package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/bardiup/bardiup/api/v1alpha1"
	"github.com/bardiup/bardiup/pkg/backup"
	"github.com/bardiup/bardiup/pkg/retention"
	"github.com/bardiup/bardiup/pkg/storage"
	"github.com/go-logr/logr"
	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// BackupConfigReconciler reconciles a BackupConfig object
type BackupConfigReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Log       logr.Logger
	scheduler *cron.Cron
	executors map[string]*backup.Executor
	retention *retention.Manager
}

// +kubebuilder:rbac:groups=bardiup.io,resources=backupconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bardiup.io,resources=backupconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=bardiup.io,resources=backupconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=bardiup.io,resources=backups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bardiup.io,resources=backups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

func (r *BackupConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("backupconfig", req.NamespacedName)

	// Fetch the BackupConfig instance
	backupConfig := &v1alpha1.BackupConfig{}
	err := r.Get(ctx, req.NamespacedName, backupConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, could have been deleted
			log.Info("BackupConfig not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Add finalizer for cleanup
	finalizerName := "bardiup.io/finalizer"
	if backupConfig.ObjectMeta.DeletionTimestamp.IsZero() {
		// The object is not being deleted
		if !controllerutil.ContainsFinalizer(backupConfig, finalizerName) {
			controllerutil.AddFinalizer(backupConfig, finalizerName)
			if err := r.Update(ctx, backupConfig); err != nil {
				return ctrl.Result{}, err
			}
		}
	} else {
		// The object is being deleted
		if controllerutil.ContainsFinalizer(backupConfig, finalizerName) {
			// Perform cleanup
			if err := r.cleanupBackupConfig(ctx, backupConfig); err != nil {
				return ctrl.Result{}, err
			}

			// Remove finalizer
			controllerutil.RemoveFinalizer(backupConfig, finalizerName)
			if err := r.Update(ctx, backupConfig); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Check if paused
	if backupConfig.Spec.Paused {
		backupConfig.Status.Phase = v1alpha1.BackupConfigPhasePaused
		return ctrl.Result{}, r.Status().Update(ctx, backupConfig)
	}

	// Initialize storage backend if not exists
	if _, exists := r.executors[backupConfig.Name]; !exists {
		storageBackend, err := r.createStorageBackend(ctx, backupConfig)
		if err != nil {
			log.Error(err, "Failed to create storage backend")
			backupConfig.Status.Phase = v1alpha1.BackupConfigPhaseError
			backupConfig.Status.Conditions = append(backupConfig.Status.Conditions, metav1.Condition{
				Type:               "StorageReady",
				Status:             metav1.ConditionFalse,
				Reason:             "StorageInitFailed",
				Message:            err.Error(),
				LastTransitionTime: metav1.Now(),
			})
			return ctrl.Result{}, r.Status().Update(ctx, backupConfig)
		}

		executor := backup.NewExecutor(r.Client, storageBackend, log)
		r.executors[backupConfig.Name] = executor
	}

	// Schedule backups
	if err := r.scheduleBackups(ctx, backupConfig); err != nil {
		log.Error(err, "Failed to schedule backups")
		backupConfig.Status.Phase = v1alpha1.BackupConfigPhaseError
		return ctrl.Result{}, r.Status().Update(ctx, backupConfig)
	}

	// Apply retention policy
	if err := r.applyRetentionPolicy(ctx, backupConfig); err != nil {
		log.Error(err, "Failed to apply retention policy")
		// Don't fail the reconciliation for retention errors
	}

	// Update status
	backupConfig.Status.Phase = v1alpha1.BackupConfigPhaseReady
	backupConfig.Status.Conditions = append(backupConfig.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "BackupConfigured",
		Message:            "Backup configuration is ready",
		LastTransitionTime: metav1.Now(),
	})

	// Calculate next scheduled time
	if schedule, err := cron.ParseStandard(backupConfig.Spec.Schedule); err == nil {
		nextTime := schedule.Next(time.Now())
		backupConfig.Status.NextScheduledTime = &metav1.Time{Time: nextTime}
	}

	if err := r.Status().Update(ctx, backupConfig); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue after 1 minute to check for scheduled backups
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

// scheduleBackups schedules backups based on the cron schedule
func (r *BackupConfigReconciler) scheduleBackups(ctx context.Context, config *v1alpha1.BackupConfig) error {
	// Check if it's time for a backup
	schedule, err := cron.ParseStandard(config.Spec.Schedule)
	if err != nil {
		return fmt.Errorf("invalid cron schedule: %w", err)
	}

	// Check last backup time
	lastBackupTime := time.Time{}
	if config.Status.LastBackupTime != nil {
		lastBackupTime = config.Status.LastBackupTime.Time
	}

	nextTime := schedule.Next(lastBackupTime)
	if time.Now().After(nextTime) {
		// Time to create a backup
		if err := r.createBackup(ctx, config); err != nil {
			return fmt.Errorf("failed to create backup: %w", err)
		}

		// Update last backup time
		config.Status.LastBackupTime = &metav1.Time{Time: time.Now()}
	}

	return nil
}

// createBackup creates a new backup for the PVCs matching the selector
func (r *BackupConfigReconciler) createBackup(ctx context.Context, config *v1alpha1.BackupConfig) error {
	// Find PVCs matching the selector
	pvcs, err := r.findMatchingPVCs(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to find matching PVCs: %w", err)
	}

	if len(pvcs) == 0 {
		r.Log.Info("No PVCs found matching selector", "config", config.Name)
		return nil
	}

	// Get existing backups for retention period determination
	existingBackups := &v1alpha1.BackupList{}
	if err := r.List(ctx, existingBackups, client.MatchingLabels{
		"bardiup.io/config": config.Name,
	}); err != nil {
		return fmt.Errorf("failed to list existing backups: %w", err)
	}

	// Create a backup for each PVC
	for _, pvc := range pvcs {
		// Determine retention period for this backup
		retentionPeriod := r.retention.DetermineRetentionPeriod(
			config.Spec.RetentionPolicy,
			existingBackups.Items,
			time.Now(),
		)

		// Calculate expiration time
		expiresAt := r.retention.CalculateExpiration(
			config.Spec.RetentionPolicy,
			retentionPeriod,
			time.Now(),
		)

		// Create backup object
		backup := &v1alpha1.Backup{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: fmt.Sprintf("%s-%s-", config.Name, pvc.Name),
				Namespace:    pvc.Namespace,
				Labels: map[string]string{
					"bardiup.io/config":    config.Name,
					"bardiup.io/pvc":       pvc.Name,
					"bardiup.io/namespace": pvc.Namespace,
					"bardiup.io/retention": retentionPeriod,
				},
			},
			Spec: v1alpha1.BackupSpec{
				BackupConfig: config.Name,
				PVCName:      pvc.Name,
				PVCNamespace: pvc.Namespace,
				StorageLocation: v1alpha1.StorageLocation{
					Type: config.Spec.StorageBackend.Type,
					Path: fmt.Sprintf("%s/%s/%s/%s-%d.tar.gz",
						config.Spec.StorageBackend.Prefix,
						pvc.Namespace,
						pvc.Name,
						pvc.Name,
						time.Now().Unix(),
					),
				},
				RetentionPeriod: retentionPeriod,
				ExpiresAt:       expiresAt,
			},
		}

		// Set owner reference
		if err := controllerutil.SetControllerReference(config, backup, r.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}

		// Create the backup
		if err := r.Create(ctx, backup); err != nil {
			return fmt.Errorf("failed to create backup: %w", err)
		}

		r.Log.Info("Created backup", "backup", backup.Name, "pvc", pvc.Name, "retention", retentionPeriod)

		// Execute the backup
		if executor, exists := r.executors[config.Name]; exists {
			go func() {
				if err := executor.ExecuteBackup(context.Background(), backup, config); err != nil {
					r.Log.Error(err, "Failed to execute backup", "backup", backup.Name)
				}
			}()
		}
	}

	// Update backup count
	config.Status.BackupCount += len(pvcs)
	config.Status.LastSuccessfulBackupTime = &metav1.Time{Time: time.Now()}

	return nil
}

// findMatchingPVCs finds PVCs that match the selector
func (r *BackupConfigReconciler) findMatchingPVCs(ctx context.Context, config *v1alpha1.BackupConfig) ([]corev1.PersistentVolumeClaim, error) {
	selector := config.Spec.PVCSelector
	pvcList := &corev1.PersistentVolumeClaimList{}

	// List PVCs in the specified namespace
	listOpts := []client.ListOption{
		client.InNamespace(selector.Namespace),
	}

	// Add label selector if specified
	if len(selector.MatchLabels) > 0 {
		listOpts = append(listOpts, client.MatchingLabels(selector.MatchLabels))
	}

	if err := r.List(ctx, pvcList, listOpts...); err != nil {
		return nil, err
	}

	// Filter by names if specified
	if len(selector.MatchNames) > 0 {
		nameMap := make(map[string]bool)
		for _, name := range selector.MatchNames {
			nameMap[name] = true
		}

		filtered := []corev1.PersistentVolumeClaim{}
		for _, pvc := range pvcList.Items {
			if nameMap[pvc.Name] {
				filtered = append(filtered, pvc)
			}
		}
		return filtered, nil
	}

	return pvcList.Items, nil
}

// applyRetentionPolicy applies retention policy to existing backups
func (r *BackupConfigReconciler) applyRetentionPolicy(ctx context.Context, config *v1alpha1.BackupConfig) error {
	// List all backups for this config
	backupList := &v1alpha1.BackupList{}
	if err := r.List(ctx, backupList, client.MatchingLabels{
		"bardiup.io/config": config.Name,
	}); err != nil {
		return fmt.Errorf("failed to list backups: %w", err)
	}

	// Apply retention policy
	toDelete, err := r.retention.ApplyRetentionPolicy(ctx, config, backupList.Items)
	if err != nil {
		return fmt.Errorf("failed to apply retention policy: %w", err)
	}

	// Delete backups that should be removed
	for _, backup := range toDelete {
		// Delete from object storage
		if executor, exists := r.executors[config.Name]; exists {
			if executor != nil {
				// Delete from storage backend
				// This would be implemented in the executor
				r.Log.Info("Would delete backup from storage", "backup", backup.Name)
			}
		}

		// Delete the backup object
		if err := r.Delete(ctx, &backup); err != nil {
			r.Log.Error(err, "Failed to delete backup", "backup", backup.Name)
			// Continue with other deletions
		} else {
			r.Log.Info("Deleted backup", "backup", backup.Name)
		}
	}

	return nil
}

// createStorageBackend creates the appropriate storage backend
func (r *BackupConfigReconciler) createStorageBackend(ctx context.Context, config *v1alpha1.BackupConfig) (storage.Backend, error) {
	switch config.Spec.StorageBackend.Type {
	case "s3":
		if config.Spec.StorageBackend.S3 == nil {
			return nil, fmt.Errorf("S3 configuration is required when type is s3")
		}
		return storage.NewS3Backend(ctx, r.Client, config.Spec.StorageBackend.S3, config.Spec.StorageBackend.Prefix, r.Log)
	default:
		return nil, fmt.Errorf("unsupported storage backend type: %s", config.Spec.StorageBackend.Type)
	}
}

// cleanupBackupConfig performs cleanup when a BackupConfig is deleted
func (r *BackupConfigReconciler) cleanupBackupConfig(ctx context.Context, config *v1alpha1.BackupConfig) error {
	// Delete all associated backups
	backupList := &v1alpha1.BackupList{}
	if err := r.List(ctx, backupList, client.MatchingLabels{
		"bardiup.io/config": config.Name,
	}); err != nil {
		return err
	}

	for _, backup := range backupList.Items {
		if err := r.Delete(ctx, &backup); err != nil && !errors.IsNotFound(err) {
			r.Log.Error(err, "Failed to delete backup during cleanup", "backup", backup.Name)
		}
	}

	// Remove executor
	delete(r.executors, config.Name)

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BackupConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Initialize fields
	r.scheduler = cron.New()
	r.executors = make(map[string]*backup.Executor)
	r.retention = retention.NewManager(r.Client, r.Log)

	// Start the scheduler
	r.scheduler.Start()

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.BackupConfig{}).
		Owns(&v1alpha1.Backup{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}
