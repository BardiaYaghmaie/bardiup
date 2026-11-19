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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/BardiaYaghmaie/bardiup/api/v1alpha1"
	"github.com/BardiaYaghmaie/bardiup/internal/agent"
)

// AnnotationRestoredFrom is set on PVCs created by a Restore.
const AnnotationRestoredFrom = "bardiup.io/restored-from"

// RestoreReconciler extracts a backup archive into a new or existing PVC.
type RestoreReconciler struct {
	client.Client
	APIReader client.Reader
	Recorder  events.EventRecorder
	Jobs      JobSettings
	Now       func() time.Time
}

// +kubebuilder:rbac:groups=bardiup.io,resources=restores,verbs=get;list;watch
// +kubebuilder:rbac:groups=bardiup.io,resources=restores/status;restores/finalizers,verbs=get;update;patch

func (r *RestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	rs := &v1alpha1.Restore{}
	if err := r.Get(ctx, req.NamespacedName, rs); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if rs.Status.Phase == v1alpha1.RestorePhaseCompleted || rs.Status.Phase == v1alpha1.RestorePhaseFailed {
		return ctrl.Result{}, nil
	}
	orig := rs.DeepCopy()
	var (
		res ctrl.Result
		err error
	)
	if rs.Status.Phase == v1alpha1.RestorePhaseRunning {
		err = r.watchJob(ctx, rs)
	} else {
		res, err = r.start(ctx, rs)
	}
	if !equality.Semantic.DeepEqual(orig.Status, rs.Status) {
		if perr := r.Status().Patch(ctx, rs, client.MergeFrom(orig)); perr != nil {
			return ctrl.Result{}, perr
		}
	}
	return res, err
}

func (r *RestoreReconciler) fail(rs *v1alpha1.Restore, reason, msg string) {
	rs.Status.Phase = v1alpha1.RestorePhaseFailed
	rs.Status.Message = truncate(msg, 1024)
	rs.Status.CompletionTime = &metav1.Time{Time: r.Now()}
	r.Recorder.Eventf(rs, nil, corev1.EventTypeWarning, reason, "Restore", "%s", rs.Status.Message)
	restoresTotal.WithLabelValues(rs.Namespace, "failed").Inc()
}

func (r *RestoreReconciler) start(ctx context.Context, rs *v1alpha1.Restore) (ctrl.Result, error) {
	var (
		s3     v1alpha1.S3Storage
		key    string
		sha    string
		source *v1alpha1.SourceVolume
		origin string
	)
	if rs.Spec.BackupName != "" {
		b := &v1alpha1.Backup{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: rs.Namespace, Name: rs.Spec.BackupName}, b); err != nil {
			if apierrors.IsNotFound(err) {
				r.fail(rs, "BackupNotFound", fmt.Sprintf("backup %q not found", rs.Spec.BackupName))
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, err
		}
		switch b.Status.Phase {
		case v1alpha1.BackupPhaseCompleted:
		case v1alpha1.BackupPhaseFailed:
			r.fail(rs, "BackupFailed", fmt.Sprintf("backup %q failed and cannot be restored", b.Name))
			return ctrl.Result{}, nil
		default:
			rs.Status.Phase = v1alpha1.RestorePhasePending
			rs.Status.Message = fmt.Sprintf("waiting for backup %q to complete", b.Name)
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}
		s3, key, sha, source, origin = b.Spec.Storage.S3, b.Status.ObjectKey, b.Status.SHA256, b.Status.Source, b.Name
	} else {
		s3, key, origin = rs.Spec.Source.Storage.S3, rs.Spec.Source.ObjectKey, rs.Spec.Source.ObjectKey
	}

	if _, err := loadCredentials(ctx, r.APIReader, rs.Namespace, s3.CredentialsSecretRef); err != nil {
		r.fail(rs, "InvalidCredentials", err.Error())
		return ctrl.Result{}, nil
	}
	if err := checkPodSecurity(ctx, r.APIReader, rs.Namespace, rs.Spec.Job); err != nil {
		r.fail(rs, "PodSecurity", err.Error())
		return ctrl.Result{}, nil
	}

	pvc, created, err := r.ensureTarget(ctx, rs, source, origin)
	if err != nil {
		return ctrl.Result{}, err
	}
	if pvc == nil {
		return ctrl.Result{}, nil
	}
	rs.Status.PVCCreated = rs.Status.PVCCreated || created

	node, err := placement(ctx, r.APIReader, pvc)
	if err != nil {
		if errors.Is(err, errRWOPInUse) {
			r.fail(rs, "VolumeInUse", err.Error())
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	jobName := boundedName("restore-"+rs.Name, 63)
	job, err := r.Jobs.build(moverJob{
		kind:      moverRestore,
		name:      jobName,
		namespace: rs.Namespace,
		pvc:       pvc.Name,
		owner:     ownerRef(rs, "Restore"),
		storage:   s3,
		opts:      rs.Spec.Job,
		node:      node,
		cfg:       agent.Config{Storage: s3, ObjectKey: key, ExpectedSHA256: sha, Clean: rs.Spec.Target.CleanTarget},
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}
	now := metav1.NewTime(r.Now())
	rs.Status.StartTime = &now
	rs.Status.JobName = jobName
	rs.Status.Phase = v1alpha1.RestorePhaseRunning
	rs.Status.Message = fmt.Sprintf("restoring %s into PVC %s", key, pvc.Name)
	r.Recorder.Eventf(rs, nil, corev1.EventTypeNormal, "Started", "Restore", "Started job %s", jobName)
	return ctrl.Result{}, nil
}

// ensureTarget returns the target PVC, creating it when missing. A nil PVC means the restore failed.
func (r *RestoreReconciler) ensureTarget(ctx context.Context, rs *v1alpha1.Restore, source *v1alpha1.SourceVolume, origin string) (*corev1.PersistentVolumeClaim, bool, error) {
	t := rs.Spec.Target
	pvc := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, client.ObjectKey{Namespace: rs.Namespace, Name: t.PVCName}, pvc)
	if err == nil {
		if pvc.Spec.VolumeMode != nil && *pvc.Spec.VolumeMode == corev1.PersistentVolumeBlock {
			r.fail(rs, "Unsupported", "target PVC is a block-mode volume")
			return nil, false, nil
		}
		return pvc, false, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, false, err
	}

	if source == nil {
		source = &v1alpha1.SourceVolume{}
	}
	var size resource.Quantity
	switch {
	case t.Size != nil:
		size = *t.Size
	case source.Capacity != "":
		q, err := resource.ParseQuantity(source.Capacity)
		if err != nil {
			r.fail(rs, "InvalidSize", fmt.Sprintf("recorded capacity %q is invalid: %v", source.Capacity, err))
			return nil, false, nil
		}
		size = q
	default:
		r.fail(rs, "SizeRequired", fmt.Sprintf("PVC %q does not exist; set spec.target.size so it can be created", t.PVCName))
		return nil, false, nil
	}
	modes := t.AccessModes
	if len(modes) == 0 {
		modes = source.AccessModes
	}
	if len(modes) == 0 {
		modes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	}
	class := t.StorageClassName
	if class == nil {
		class = source.StorageClassName
	}
	pvc = &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        t.PVCName,
			Namespace:   rs.Namespace,
			Annotations: map[string]string{AnnotationRestoredFrom: origin},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: class,
			AccessModes:      modes,
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: size}},
		},
	}
	if err := r.Create(ctx, pvc); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, false, err
	}
	r.Recorder.Eventf(rs, nil, corev1.EventTypeNormal, "PVCCreated", "Restore", "Created PVC %s (%s)", pvc.Name, size.String())
	return pvc, true, nil
}

func (r *RestoreReconciler) watchJob(ctx context.Context, rs *v1alpha1.Restore) error {
	job := &batchv1.Job{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: rs.Namespace, Name: rs.Status.JobName}, job); err != nil {
		if apierrors.IsNotFound(err) {
			r.fail(rs, "JobLost", fmt.Sprintf("job %s was deleted before finishing", rs.Status.JobName))
			return nil
		}
		return err
	}
	done, res, failure := jobOutcome(ctx, r.APIReader, job)
	if !done {
		return nil
	}
	if res == nil {
		r.fail(rs, "JobFailed", failure)
		return nil
	}
	now := metav1.NewTime(r.Now())
	rs.Status.Phase = v1alpha1.RestorePhaseCompleted
	rs.Status.CompletionTime = &now
	rs.Status.Files = res.Files
	rs.Status.Message = "restore completed, checksum verified"
	if len(res.Warnings) > 0 {
		rs.Status.Message += fmt.Sprintf("; %d warning(s), first: %s", len(res.Warnings), res.Warnings[0])
	}
	restoresTotal.WithLabelValues(rs.Namespace, "completed").Inc()
	r.Recorder.Eventf(rs, nil, corev1.EventTypeNormal, "Completed", "Restore", "Restored %d files into PVC %s", rs.Status.Files, rs.Spec.Target.PVCName)
	if err := r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
		log.FromContext(ctx).Error(err, "delete finished job")
	}
	return nil
}

// SetupWithManager registers the controller.
func (r *RestoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Restore{}).
		Owns(&batchv1.Job{}).
		Named("restore").
		Complete(r)
}
