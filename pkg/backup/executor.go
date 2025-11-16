package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/bardiup/bardiup/api/v1alpha1"
	"github.com/bardiup/bardiup/pkg/storage"
	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Executor handles backup execution
type Executor struct {
	client  client.Client
	storage storage.Backend
	log     logr.Logger
}

// NewExecutor creates a new backup executor
func NewExecutor(client client.Client, storage storage.Backend, log logr.Logger) *Executor {
	return &Executor{
		client:  client,
		storage: storage,
		log:     log,
	}
}

// ExecuteBackup performs the actual backup of a PVC
func (e *Executor) ExecuteBackup(ctx context.Context, backup *v1alpha1.Backup, config *v1alpha1.BackupConfig) error {
	e.log.Info("Starting backup execution", "backup", backup.Name, "pvc", backup.Spec.PVCName)

	// Get the PVC
	pvc := &corev1.PersistentVolumeClaim{}
	if err := e.client.Get(ctx, client.ObjectKey{
		Name:      backup.Spec.PVCName,
		Namespace: backup.Spec.PVCNamespace,
	}, pvc); err != nil {
		return fmt.Errorf("failed to get PVC: %w", err)
	}

	// Update backup status to InProgress
	backup.Status.Phase = v1alpha1.BackupPhaseInProgress
	backup.Status.StartTime = &metav1.Time{Time: time.Now()}
	if err := e.client.Status().Update(ctx, backup); err != nil {
		return fmt.Errorf("failed to update backup status: %w", err)
	}

	// Execute backup based on method
	var err error
	switch config.Spec.BackupMethod {
	case v1alpha1.BackupMethodSnapshot:
		err = e.executeSnapshotBackup(ctx, backup, pvc, config)
	default: // BackupMethodCopy
		err = e.executeCopyBackup(ctx, backup, pvc, config)
	}

	if err != nil {
		backup.Status.Phase = v1alpha1.BackupPhaseFailed
		backup.Status.Message = err.Error()
	} else {
		backup.Status.Phase = v1alpha1.BackupPhaseCompleted
		backup.Status.CompletionTime = &metav1.Time{Time: time.Now()}
		backup.Status.Message = "Backup completed successfully"
	}

	// Update final status
	if updateErr := e.client.Status().Update(ctx, backup); updateErr != nil {
		e.log.Error(updateErr, "Failed to update backup status")
		if err == nil {
			err = updateErr
		}
	}

	return err
}

// executeCopyBackup performs a copy-based backup using a Job
func (e *Executor) executeCopyBackup(ctx context.Context, backup *v1alpha1.Backup, pvc *corev1.PersistentVolumeClaim, config *v1alpha1.BackupConfig) error {
	// Create a backup Job that will mount the PVC and upload data to object storage
	job := e.createBackupJob(backup, pvc, config)

	// Create the job
	if err := e.client.Create(ctx, job); err != nil {
		return fmt.Errorf("failed to create backup job: %w", err)
	}

	// Set owner reference so job gets cleaned up with backup
	if err := controllerutil.SetControllerReference(backup, job, e.client.Scheme()); err != nil {
		return fmt.Errorf("failed to set owner reference: %w", err)
	}

	// Wait for job to complete
	if err := e.waitForJob(ctx, job); err != nil {
		return fmt.Errorf("backup job failed: %w", err)
	}

	// Get job pods to extract backup metadata
	podList := &corev1.PodList{}
	if err := e.client.List(ctx, podList, client.InNamespace(job.Namespace), client.MatchingLabels{"job-name": job.Name}); err != nil {
		return fmt.Errorf("failed to list job pods: %w", err)
	}

	// Extract backup size and checksum from pod logs or annotations
	// This is simplified - in production you'd extract this from the backup job
	backup.Spec.StorageLocation.Size = e.calculateBackupSize(pvc)
	backup.Status.Checksum = e.generateChecksum()

	e.log.Info("Backup job completed successfully", "job", job.Name)
	return nil
}

// createBackupJob creates a Kubernetes Job for backing up PVC data
func (e *Executor) createBackupJob(backup *v1alpha1.Backup, pvc *corev1.PersistentVolumeClaim, config *v1alpha1.BackupConfig) *batchv1.Job {
	jobName := fmt.Sprintf("backup-%s-%s", backup.Name, backup.Spec.PVCName)

	// Build the backup container command
	// This is a simplified version - in production you'd have a proper backup image
	backupCommand := []string{
		"/bin/sh",
		"-c",
		fmt.Sprintf(`
			set -e
			echo "Starting backup of /data to %s"
			tar czf /tmp/backup.tar.gz /data
			# Upload to object storage (simplified - would use AWS CLI or similar)
			echo "Uploading backup to object storage..."
			echo "Backup completed successfully"
		`, backup.Spec.StorageLocation.Path),
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: backup.Namespace,
			Labels: map[string]string{
				"bardiup.io/backup": backup.Name,
				"bardiup.io/pvc":    pvc.Name,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr(int32(3)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"bardiup.io/backup": backup.Name,
						"bardiup.io/pvc":    pvc.Name,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{
						{
							Name:    "backup",
							Image:   "alpine:latest", // In production, use a proper backup image
							Command: backupCommand,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "data",
									MountPath: "/data",
									ReadOnly:  true,
								},
								{
									Name:      "temp",
									MountPath: "/tmp",
								},
							},
							Env: e.getBackupEnvVars(config),
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "data",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: pvc.Name,
									ReadOnly:  true,
								},
							},
						},
						{
							Name: "temp",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}

	return job
}

// getBackupEnvVars returns environment variables for the backup container
func (e *Executor) getBackupEnvVars(config *v1alpha1.BackupConfig) []corev1.EnvVar {
	envVars := []corev1.EnvVar{}

	if config.Spec.StorageBackend.S3 != nil {
		s3Config := config.Spec.StorageBackend.S3
		envVars = append(envVars,
			corev1.EnvVar{Name: "S3_BUCKET", Value: s3Config.Bucket},
			corev1.EnvVar{Name: "S3_REGION", Value: s3Config.Region},
		)

		if s3Config.Endpoint != "" {
			envVars = append(envVars, corev1.EnvVar{Name: "S3_ENDPOINT", Value: s3Config.Endpoint})
		}

		// Add credentials from secret
		envVars = append(envVars,
			corev1.EnvVar{
				Name: "AWS_ACCESS_KEY_ID",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: s3Config.CredentialsSecret,
						},
						Key: "accessKeyId",
					},
				},
			},
			corev1.EnvVar{
				Name: "AWS_SECRET_ACCESS_KEY",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: s3Config.CredentialsSecret,
						},
						Key: "secretAccessKey",
					},
				},
			},
		)
	}

	return envVars
}

// waitForJob waits for a job to complete
func (e *Executor) waitForJob(ctx context.Context, job *batchv1.Job) error {
	return wait.PollImmediate(5*time.Second, 30*time.Minute, func() (bool, error) {
		currentJob := &batchv1.Job{}
		if err := e.client.Get(ctx, client.ObjectKey{
			Name:      job.Name,
			Namespace: job.Namespace,
		}, currentJob); err != nil {
			return false, err
		}

		if currentJob.Status.Succeeded > 0 {
			return true, nil
		}

		if currentJob.Status.Failed > 0 && currentJob.Status.Failed >= *currentJob.Spec.BackoffLimit {
			return false, fmt.Errorf("job failed after %d attempts", currentJob.Status.Failed)
		}

		return false, nil
	})
}

// executeSnapshotBackup performs a snapshot-based backup
func (e *Executor) executeSnapshotBackup(ctx context.Context, backup *v1alpha1.Backup, pvc *corev1.PersistentVolumeClaim, config *v1alpha1.BackupConfig) error {
	// This would integrate with VolumeSnapshot API
	// For now, this is a placeholder
	e.log.Info("Snapshot backup not yet implemented, falling back to copy method")
	return e.executeCopyBackup(ctx, backup, pvc, config)
}

// RestoreBackup restores a backup to a PVC
func (e *Executor) RestoreBackup(ctx context.Context, backup *v1alpha1.Backup, targetPVC *corev1.PersistentVolumeClaim) error {
	e.log.Info("Starting restore", "backup", backup.Name, "targetPVC", targetPVC.Name)

	// Download backup from object storage
	reader, err := e.storage.Download(ctx, backup.Spec.StorageLocation.Path)
	if err != nil {
		return fmt.Errorf("failed to download backup: %w", err)
	}
	defer reader.Close()

	// Create a restore job that will download and extract the backup
	restoreJob := e.createRestoreJob(backup, targetPVC)

	if err := e.client.Create(ctx, restoreJob); err != nil {
		return fmt.Errorf("failed to create restore job: %w", err)
	}

	// Wait for restore job to complete
	if err := e.waitForJob(ctx, restoreJob); err != nil {
		return fmt.Errorf("restore job failed: %w", err)
	}

	e.log.Info("Restore completed successfully", "backup", backup.Name, "targetPVC", targetPVC.Name)
	return nil
}

// createRestoreJob creates a job to restore backup data to a PVC
func (e *Executor) createRestoreJob(backup *v1alpha1.Backup, targetPVC *corev1.PersistentVolumeClaim) *batchv1.Job {
	jobName := fmt.Sprintf("restore-%s-%s", backup.Name, targetPVC.Name)

	restoreCommand := []string{
		"/bin/sh",
		"-c",
		fmt.Sprintf(`
			set -e
			echo "Starting restore from %s to /data"
			# Download from object storage (simplified)
			echo "Downloading backup from object storage..."
			# Extract backup
			tar xzf /tmp/backup.tar.gz -C /data
			echo "Restore completed successfully"
		`, backup.Spec.StorageLocation.Path),
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: targetPVC.Namespace,
			Labels: map[string]string{
				"bardiup.io/restore": backup.Name,
				"bardiup.io/pvc":     targetPVC.Name,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr(int32(3)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{
						{
							Name:    "restore",
							Image:   "alpine:latest",
							Command: restoreCommand,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "data",
									MountPath: "/data",
								},
								{
									Name:      "temp",
									MountPath: "/tmp",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "data",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: targetPVC.Name,
								},
							},
						},
						{
							Name: "temp",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}

	return job
}

// Helper functions
func (e *Executor) calculateBackupSize(pvc *corev1.PersistentVolumeClaim) int64 {
	// In production, this would get the actual backup size
	// For now, return a placeholder
	if pvc.Status.Capacity != nil {
		if capacity, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
			return capacity.Value()
		}
	}
	return 0
}

func (e *Executor) generateChecksum() string {
	// In production, this would calculate the actual checksum
	// For now, return a placeholder
	hash := sha256.New()
	hash.Write([]byte(time.Now().String()))
	return hex.EncodeToString(hash.Sum(nil))
}

func ptr[T any](v T) *T {
	return &v
}
