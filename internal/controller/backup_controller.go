package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/BardiaYaghmaie/bardiup/api/v1alpha1"
	"github.com/BardiaYaghmaie/bardiup/internal/agent"
	"github.com/BardiaYaghmaie/bardiup/internal/storage"
)

// BackupReconciler drives a Backup through snapshot (optional), data copy and completion,
// and removes stored data when the Backup is deleted.
type BackupReconciler struct {
	client.Client
	APIReader client.Reader
	Recorder  events.EventRecorder
	Jobs      JobSettings
	Stores    StoreFactory
	Now       func() time.Time
}

// +kubebuilder:rbac:groups=bardiup.io,resources=backups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bardiup.io,resources=backups/status;backups/finalizers,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=list
// +kubebuilder:rbac:groups="",resources=secrets;namespaces,verbs=get
// +kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshots,verbs=get;create;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

func (r *BackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	b := &v1alpha1.Backup{}
	if err := r.Get(ctx, req.NamespacedName, b); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !b.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, b)
	}
	if controllerutil.AddFinalizer(b, v1alpha1.Finalizer) {
		if err := r.Update(ctx, b); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(ignoreConflict(err))
		}
	}

	orig := b.DeepCopy()
	res, err := r.step(ctx, b)
	if !equality.Semantic.DeepEqual(orig.Status, b.Status) {
		written, perr := patchStatus(ctx, r.Client, b, orig)
		if perr != nil || !written {
			return ctrl.Result{}, perr
		}
		if orig.Status.Phase != b.Status.Phase {
			r.transitioned(ctx, b)
		}
	}
	return res, err
}

// transitioned runs the side effects of entering a terminal phase, only after that phase is persisted.
func (r *BackupReconciler) transitioned(ctx context.Context, b *v1alpha1.Backup) {
	cfg := b.Labels[v1alpha1.LabelBackupConfig]
	switch b.Status.Phase {
	case v1alpha1.BackupPhaseCompleted:
		backupsTotal.WithLabelValues(b.Namespace, cfg, "completed").Inc()
		if b.Status.StartTime != nil {
			backupDuration.WithLabelValues(b.Namespace, cfg).Observe(b.Status.CompletionTime.Sub(b.Status.StartTime.Time).Seconds())
		}
		r.Recorder.Eventf(b, nil, corev1.EventTypeNormal, "Completed", "Backup", "Stored %s (%d files) at %s", b.Status.Size, b.Status.Files, b.Status.ObjectKey)
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: b.Status.JobName, Namespace: b.Namespace}}
		if err := r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
			log.FromContext(ctx).Error(err, "delete finished job")
		}
	case v1alpha1.BackupPhaseFailed:
		backupsTotal.WithLabelValues(b.Namespace, cfg, "failed").Inc()
		reason := "Failed"
		if c := meta.FindStatusCondition(b.Status.Conditions, "Succeeded"); c != nil {
			reason = c.Reason
		}
		r.Recorder.Eventf(b, nil, corev1.EventTypeWarning, reason, "Backup", "%s", b.Status.Message)
	default:
		return
	}
	if err := deleteSnapshotResources(ctx, r.Client, b); err != nil {
		log.FromContext(ctx).Error(err, "cleanup snapshot resources")
	}
}

func (r *BackupReconciler) fail(b *v1alpha1.Backup, reason, msg string) {
	b.Status.Phase = v1alpha1.BackupPhaseFailed
	b.Status.Message = truncate(msg, 1024)
	b.Status.CompletionTime = &metav1.Time{Time: r.Now()}
	meta.SetStatusCondition(&b.Status.Conditions, metav1.Condition{
		Type: "Succeeded", Status: metav1.ConditionFalse, Reason: reason, Message: b.Status.Message, ObservedGeneration: b.Generation,
	})
}

func (r *BackupReconciler) step(ctx context.Context, b *v1alpha1.Backup) (ctrl.Result, error) {
	switch b.Status.Phase {
	case "":
		return r.initialize(ctx, b)
	case v1alpha1.BackupPhasePending:
		if b.Spec.Method == v1alpha1.BackupMethodSnapshot {
			return r.startSnapshot(ctx, b)
		}
		return r.startJob(ctx, b, b.Spec.PVCName)
	case v1alpha1.BackupPhaseSnapshotting:
		return r.waitSnapshot(ctx, b)
	case v1alpha1.BackupPhaseRunning:
		return r.watchJob(ctx, b)
	}
	return ctrl.Result{}, nil
}

func (r *BackupReconciler) initialize(ctx context.Context, b *v1alpha1.Backup) (ctrl.Result, error) {
	now := metav1.NewTime(r.Now())
	b.Status.StartTime = &now
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: b.Namespace, Name: b.Spec.PVCName}, pvc); err != nil {
		if apierrors.IsNotFound(err) {
			r.fail(b, "PVCNotFound", fmt.Sprintf("PVC %q not found", b.Spec.PVCName))
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if pvc.Spec.VolumeMode != nil && *pvc.Spec.VolumeMode == corev1.PersistentVolumeBlock {
		r.fail(b, "Unsupported", "block-mode volumes are not supported; only filesystem volumes can be backed up")
		return ctrl.Result{}, nil
	}
	if _, err := loadCredentials(ctx, r.APIReader, b.Namespace, b.Spec.Storage.S3.CredentialsSecretRef); err != nil {
		r.fail(b, "InvalidCredentials", err.Error())
		return ctrl.Result{}, nil
	}
	if err := checkPodSecurity(ctx, r.APIReader, b.Namespace, b.Spec.Job); err != nil {
		r.fail(b, "PodSecurity", err.Error())
		return ctrl.Result{}, nil
	}
	src := &v1alpha1.SourceVolume{
		StorageClassName: pvc.Spec.StorageClassName,
		AccessModes:      pvc.Spec.AccessModes,
	}
	if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
		src.Capacity = q.String()
	} else if q, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		src.Capacity = q.String()
	}
	b.Status.Source = src
	b.Status.ObjectKey = storage.ObjectKey(b.Spec.Storage.S3.Prefix, b.Namespace, b.Spec.PVCName, b.Name)
	b.Status.Phase = v1alpha1.BackupPhasePending
	b.Status.Message = ""
	return ctrl.Result{}, nil
}

func (r *BackupReconciler) startSnapshot(ctx context.Context, b *v1alpha1.Backup) (ctrl.Result, error) {
	snap, _ := snapshotNames(b)
	var class *string
	if b.Spec.Snapshot != nil {
		class = b.Spec.Snapshot.VolumeSnapshotClassName
	}
	vs := newVolumeSnapshot(snap, b.Namespace, b.Spec.PVCName, class, ownerRef(b, "Backup"))
	if err := r.Create(ctx, vs); err != nil && !apierrors.IsAlreadyExists(err) {
		if meta.IsNoMatchError(err) {
			r.fail(b, "SnapshotsUnavailable", "VolumeSnapshot API (snapshot.storage.k8s.io/v1) is not installed in this cluster; use method: copy")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	b.Status.SnapshotName = snap
	b.Status.Phase = v1alpha1.BackupPhaseSnapshotting
	b.Status.Message = "waiting for VolumeSnapshot to become ready"
	r.Recorder.Eventf(b, nil, corev1.EventTypeNormal, "SnapshotCreated", "Backup", "Created VolumeSnapshot %s", snap)
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func (r *BackupReconciler) waitSnapshot(ctx context.Context, b *v1alpha1.Backup) (ctrl.Result, error) {
	snap, clone := snapshotNames(b)
	st, err := getSnapshotState(ctx, r.APIReader, b.Namespace, snap)
	if err != nil {
		if apierrors.IsNotFound(err) {
			r.fail(b, "SnapshotLost", fmt.Sprintf("VolumeSnapshot %s disappeared", snap))
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if st.err != "" {
		r.fail(b, "SnapshotFailed", st.err)
		return ctrl.Result{}, nil
	}
	if !st.ready {
		if b.Status.StartTime != nil && r.Now().Sub(b.Status.StartTime.Time) > 30*time.Minute {
			r.fail(b, "SnapshotTimeout", fmt.Sprintf("VolumeSnapshot %s not ready after 30m", snap))
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	src := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: b.Namespace, Name: b.Spec.PVCName}, src); err != nil {
		if apierrors.IsNotFound(err) {
			r.fail(b, "PVCNotFound", fmt.Sprintf("PVC %q not found", b.Spec.PVCName))
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	pvc := clonePVC(clone, src, snap, st.restoreSize, ownerRef(b, "Backup"))
	if err := r.Create(ctx, pvc); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}
	return r.startJob(ctx, b, clone)
}

func (r *BackupReconciler) startJob(ctx context.Context, b *v1alpha1.Backup, pvcName string) (ctrl.Result, error) {
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: b.Namespace, Name: pvcName}, pvc); err != nil {
		if apierrors.IsNotFound(err) && pvcName == b.Spec.PVCName {
			r.fail(b, "PVCNotFound", fmt.Sprintf("PVC %q not found", pvcName))
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	node := ""
	if pvcName == b.Spec.PVCName {
		n, err := placement(ctx, r.APIReader, pvc)
		if err != nil {
			if errors.Is(err, errRWOPInUse) {
				r.fail(b, "VolumeInUse", err.Error())
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, err
		}
		node = n
	}

	var sizeHint int64
	if q, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		sizeHint = q.Value()
	}
	if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok && q.Value() > sizeHint {
		sizeHint = q.Value()
	}
	jobName := boundedName("backup-"+b.Name, 63)
	job, err := r.Jobs.build(moverJob{
		kind:      moverBackup,
		name:      jobName,
		namespace: b.Namespace,
		pvc:       pvcName,
		owner:     ownerRef(b, "Backup"),
		storage:   b.Spec.Storage.S3,
		opts:      b.Spec.Job,
		node:      node,
		cfg: agent.Config{
			Storage:   b.Spec.Storage.S3,
			ObjectKey: b.Status.ObjectKey,
			Excludes:  b.Spec.Excludes,
			SizeHint:  sizeHint,
			Manifest: &agent.Manifest{
				Backup:       b.Name,
				BackupConfig: b.Labels[v1alpha1.LabelBackupConfig],
				Namespace:    b.Namespace,
				PVC:          b.Spec.PVCName,
				Source:       b.Status.Source,
				Method:       methodOrDefault(b.Spec.Method),
			},
		},
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}
	b.Status.JobName = jobName
	b.Status.Phase = v1alpha1.BackupPhaseRunning
	b.Status.Message = "copying data"
	if node != "" {
		b.Status.Message = fmt.Sprintf("copying data on node %s next to the workload using the volume", node)
	}
	r.Recorder.Eventf(b, nil, corev1.EventTypeNormal, "Started", "Backup", "Started job %s", jobName)
	return ctrl.Result{}, nil
}

func methodOrDefault(m v1alpha1.BackupMethod) v1alpha1.BackupMethod {
	if m == "" {
		return v1alpha1.BackupMethodCopy
	}
	return m
}

func (r *BackupReconciler) watchJob(ctx context.Context, b *v1alpha1.Backup) (ctrl.Result, error) {
	job := &batchv1.Job{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: b.Namespace, Name: b.Status.JobName}, job); err != nil {
		if apierrors.IsNotFound(err) {
			r.fail(b, "JobLost", fmt.Sprintf("job %s was deleted before finishing", b.Status.JobName))
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	done, res, failure := jobOutcome(ctx, r.APIReader, job)
	if !done {
		return ctrl.Result{}, nil
	}
	if res == nil {
		r.fail(b, "JobFailed", failure)
		return ctrl.Result{}, nil
	}

	now := metav1.NewTime(r.Now())
	b.Status.Phase = v1alpha1.BackupPhaseCompleted
	b.Status.CompletionTime = &now
	b.Status.SHA256 = res.SHA256
	b.Status.SizeBytes = res.SizeBytes
	b.Status.Size = HumanBytes(res.SizeBytes)
	b.Status.Files = res.Files
	b.Status.Message = fmt.Sprintf("%d files, %s of data", res.Files, HumanBytes(res.DataBytes))
	if len(res.Warnings) > 0 {
		b.Status.Message += fmt.Sprintf("; %d warning(s), first: %s", len(res.Warnings), res.Warnings[0])
	}
	meta.SetStatusCondition(&b.Status.Conditions, metav1.Condition{
		Type: "Succeeded", Status: metav1.ConditionTrue, Reason: "Completed", Message: "backup stored", ObservedGeneration: b.Generation,
	})
	return ctrl.Result{}, nil
}

func (r *BackupReconciler) finalize(ctx context.Context, b *v1alpha1.Backup) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(b, v1alpha1.Finalizer) {
		return ctrl.Result{}, nil
	}
	if err := deleteSnapshotResources(ctx, r.Client, b); err != nil {
		return ctrl.Result{}, err
	}
	if b.Spec.DeletionPolicy != v1alpha1.DeletionPolicyRetain && b.Status.ObjectKey != "" {
		creds, err := loadCredentials(ctx, r.APIReader, b.Namespace, b.Spec.Storage.S3.CredentialsSecretRef)
		if err != nil {
			r.Recorder.Eventf(b, nil, corev1.EventTypeWarning, "DeleteFailed", "Delete", "cannot delete stored data: %v; set spec.deletionPolicy=Retain to remove the Backup without deleting data", err)
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		store, err := r.Stores(ctx, b.Spec.Storage.S3, creds)
		if err == nil {
			err = store.Delete(ctx, b.Status.ObjectKey, storage.ManifestKey(b.Status.ObjectKey))
		}
		if err != nil {
			r.Recorder.Eventf(b, nil, corev1.EventTypeWarning, "DeleteFailed", "Delete", "deleting %s: %v", b.Status.ObjectKey, err)
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
	}
	controllerutil.RemoveFinalizer(b, v1alpha1.Finalizer)
	return ctrl.Result{}, client.IgnoreNotFound(ignoreConflict(r.Update(ctx, b)))
}

// SetupWithManager registers the controller.
func (r *BackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Backup{}).
		Owns(&batchv1.Job{}).
		Named("backup").
		Complete(r)
}
