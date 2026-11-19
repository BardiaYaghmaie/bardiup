package controller

import (
	"context"
	"fmt"
	"path"
	"slices"
	"sort"
	"time"

	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/BardiaYaghmaie/bardiup/api/v1alpha1"
	"github.com/BardiaYaghmaie/bardiup/internal/retention"
)

// BackupConfigReconciler creates Backups on schedule and prunes them according to the retention policy.
type BackupConfigReconciler struct {
	client.Client
	APIReader client.Reader
	Recorder  events.EventRecorder
	Stores    StoreFactory
	Now       func() time.Time
}

// +kubebuilder:rbac:groups=bardiup.io,resources=backupconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=bardiup.io,resources=backupconfigs/status,verbs=get;update;patch

func (r *BackupConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cfg := &v1alpha1.BackupConfig{}
	if err := r.Get(ctx, req.NamespacedName, cfg); err != nil {
		if apierrors.IsNotFound(err) {
			forgetConfigMetrics(req.Namespace, req.Name)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	orig := cfg.DeepCopy()
	res, err := r.reconcile(ctx, cfg)
	cfg.Status.ObservedGeneration = cfg.Generation
	if !equality.Semantic.DeepEqual(orig.Status, cfg.Status) {
		if _, perr := patchStatus(ctx, r.Client, cfg, orig); perr != nil {
			return ctrl.Result{}, perr
		}
	}
	return res, err
}

func (r *BackupConfigReconciler) setReady(cfg *v1alpha1.BackupConfig, ok bool, reason, msg string) {
	status, v := metav1.ConditionFalse, 0.0
	if ok {
		status, v = metav1.ConditionTrue, 1
	}
	meta.SetStatusCondition(&cfg.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: status, Reason: reason, Message: msg, ObservedGeneration: cfg.Generation,
	})
	configReady.WithLabelValues(cfg.Namespace, cfg.Name).Set(v)
}

func validate(spec *v1alpha1.BackupConfigSpec) (*time.Location, cron.Schedule, labels.Selector, error) {
	loc := time.UTC
	if spec.TimeZone != "" {
		l, err := time.LoadLocation(spec.TimeZone)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("invalid timeZone %q: %w", spec.TimeZone, err)
		}
		loc = l
	}
	sched, err := cron.ParseStandard(spec.Schedule)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid schedule %q: %w", spec.Schedule, err)
	}
	if err := retention.Validate(spec.Retention); err != nil {
		return nil, nil, nil, err
	}
	for _, p := range spec.Excludes {
		if _, err := path.Match(p, ""); err != nil {
			return nil, nil, nil, fmt.Errorf("invalid exclude pattern %q: %w", p, err)
		}
	}
	sel, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels: spec.PVCSelector.MatchLabels, MatchExpressions: spec.PVCSelector.MatchExpressions,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid pvcSelector: %w", err)
	}
	return loc, sched, sel, nil
}

func (r *BackupConfigReconciler) reconcile(ctx context.Context, cfg *v1alpha1.BackupConfig) (ctrl.Result, error) {
	loc, sched, sel, err := validate(&cfg.Spec)
	if err != nil {
		r.setReady(cfg, false, "InvalidSpec", err.Error())
		return ctrl.Result{}, nil
	}

	retry := time.Duration(0)
	if err := r.checkStorage(ctx, cfg); err != nil {
		r.setReady(cfg, false, "StorageUnavailable", err.Error())
		retry = time.Minute
	} else {
		r.setReady(cfg, true, "Ready", "schedule, retention and storage are valid")
	}

	pvcs, err := r.selectPVCs(ctx, cfg, sel)
	if err != nil {
		return ctrl.Result{}, err
	}
	list := &v1alpha1.BackupList{}
	if err := r.List(ctx, list, client.InNamespace(cfg.Namespace), client.MatchingLabels{v1alpha1.LabelBackupConfig: cfg.Name}); err != nil {
		return ctrl.Result{}, err
	}
	backups := slices.DeleteFunc(list.Items, func(b v1alpha1.Backup) bool { return !b.DeletionTimestamp.IsZero() })

	active := map[string]bool{}
	for _, b := range backups {
		if b.Status.Phase != v1alpha1.BackupPhaseCompleted && b.Status.Phase != v1alpha1.BackupPhaseFailed {
			active[b.Spec.PVCName] = true
		}
	}

	now := r.Now()
	if trigger := cfg.Annotations[v1alpha1.AnnotationBackupNow]; trigger != "" && trigger != cfg.Status.LastBackupNowTrigger {
		created, err := r.run(ctx, cfg, pvcs, active, "m"+shortHash(trigger))
		if err != nil {
			return ctrl.Result{}, err
		}
		cfg.Status.LastBackupNowTrigger = trigger
		backups = append(backups, created...)
	}

	last := cfg.CreationTimestamp.Time
	if cfg.Status.LastScheduleTime != nil {
		last = cfg.Status.LastScheduleTime.Time
	}
	next := sched.Next(last.In(loc))
	if !cfg.Spec.Suspend && !now.Before(next) {
		// Missed runs (controller downtime) collapse into this one.
		created, err := r.run(ctx, cfg, pvcs, active, next.UTC().Format("20060102-150405"))
		if err != nil {
			return ctrl.Result{}, err
		}
		cfg.Status.LastScheduleTime = &metav1.Time{Time: now}
		next = sched.Next(now.In(loc))
		backups = append(backups, created...)
	}
	cfg.Status.NextScheduleTime = nil
	if !cfg.Spec.Suspend {
		cfg.Status.NextScheduleTime = &metav1.Time{Time: next}
	}

	backups, err = r.prune(ctx, cfg, backups, loc)
	if err != nil {
		return ctrl.Result{}, err
	}
	r.summarize(cfg, backups, len(pvcs))

	wait := next.Sub(now)
	if cfg.Spec.Suspend || (retry > 0 && retry < wait) {
		wait = retry
	}
	if wait <= 0 && !cfg.Spec.Suspend {
		wait = time.Second
	}
	return ctrl.Result{RequeueAfter: wait}, nil
}

// checkStorage verifies bucket access when the spec changed or the last check failed.
func (r *BackupConfigReconciler) checkStorage(ctx context.Context, cfg *v1alpha1.BackupConfig) error {
	if cfg.Status.ObservedGeneration == cfg.Generation && meta.IsStatusConditionTrue(cfg.Status.Conditions, "Ready") {
		return nil
	}
	creds, err := loadCredentials(ctx, r.APIReader, cfg.Namespace, cfg.Spec.Storage.S3.CredentialsSecretRef)
	if err != nil {
		return err
	}
	store, err := r.Stores(ctx, cfg.Spec.Storage.S3, creds)
	if err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return store.Check(checkCtx)
}

func (r *BackupConfigReconciler) selectPVCs(ctx context.Context, cfg *v1alpha1.BackupConfig, sel labels.Selector) ([]corev1.PersistentVolumeClaim, error) {
	list := &corev1.PersistentVolumeClaimList{}
	if err := r.List(ctx, list, client.InNamespace(cfg.Namespace), client.MatchingLabelsSelector{Selector: sel}); err != nil {
		return nil, err
	}
	names := cfg.Spec.PVCSelector.Names
	return slices.DeleteFunc(list.Items, func(p corev1.PersistentVolumeClaim) bool {
		return !p.DeletionTimestamp.IsZero() ||
			p.Status.Phase != corev1.ClaimBound ||
			p.Labels[v1alpha1.LabelManagedBy] == v1alpha1.ManagedByValue ||
			(len(names) > 0 && !slices.Contains(names, p.Name))
	}), nil
}

// run creates one Backup per selected PVC that has no backup in flight. Names are derived from
// the run stamp, so a retried reconcile never creates duplicates.
func (r *BackupConfigReconciler) run(ctx context.Context, cfg *v1alpha1.BackupConfig, pvcs []corev1.PersistentVolumeClaim, active map[string]bool, stamp string) ([]v1alpha1.Backup, error) {
	var created []v1alpha1.Backup
	skipped := 0
	for _, pvc := range pvcs {
		if active[pvc.Name] {
			skipped++
			continue
		}
		b := v1alpha1.Backup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      boundedName(fmt.Sprintf("%s-%s-%s", cfg.Name, pvc.Name, stamp), 253),
				Namespace: cfg.Namespace,
				Labels: map[string]string{
					v1alpha1.LabelBackupConfig: cfg.Name,
					v1alpha1.LabelPVC:          labelValue(pvc.Name),
				},
			},
			Spec: v1alpha1.BackupSpec{
				PVCName:        pvc.Name,
				Storage:        cfg.Spec.Storage,
				Method:         cfg.Spec.Method,
				Snapshot:       cfg.Spec.Snapshot,
				Excludes:       cfg.Spec.Excludes,
				Job:            cfg.Spec.Job,
				DeletionPolicy: v1alpha1.DeletionPolicyDelete,
			},
		}
		if err := r.Create(ctx, &b); err != nil {
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			return created, fmt.Errorf("create backup for PVC %s: %w", pvc.Name, err)
		}
		active[pvc.Name] = true
		created = append(created, b)
	}
	if len(created) > 0 || skipped > 0 {
		r.Recorder.Eventf(cfg, nil, corev1.EventTypeNormal, "BackupsStarted", "Schedule",
			"Started %d backup(s), skipped %d PVC(s) with a backup still running", len(created), skipped)
	}
	return created, nil
}

// prune deletes completed backups no retention rule keeps and failed backups beyond the history
// limit, records why each kept backup is retained, and returns the remaining backups.
func (r *BackupConfigReconciler) prune(ctx context.Context, cfg *v1alpha1.BackupConfig, backups []v1alpha1.Backup, loc *time.Location) ([]v1alpha1.Backup, error) {
	completed := map[string][]retention.Item{}
	failed := map[string][]v1alpha1.Backup{}
	for _, b := range backups {
		switch b.Status.Phase {
		case v1alpha1.BackupPhaseCompleted:
			completed[b.Spec.PVCName] = append(completed[b.Spec.PVCName], retention.Item{Name: b.Name, Time: b.CreationTimestamp.Time})
		case v1alpha1.BackupPhaseFailed:
			failed[b.Spec.PVCName] = append(failed[b.Spec.PVCName], b)
		}
	}

	keep := map[string][]string{}
	for _, items := range completed {
		k, err := retention.Evaluate(cfg.Spec.Retention, items, loc)
		if err != nil {
			return nil, err
		}
		for name, reasons := range k {
			keep[name] = reasons
		}
	}
	doomed := map[string]bool{}
	for _, list := range failed {
		sort.Slice(list, func(i, j int) bool { return list[j].CreationTimestamp.Before(&list[i].CreationTimestamp) })
		limit := 3
		if cfg.Spec.FailedBackupsHistoryLimit != nil {
			limit = int(*cfg.Spec.FailedBackupsHistoryLimit)
		}
		for _, b := range list[min(limit, len(list)):] {
			doomed[b.Name] = true
		}
	}

	remaining := backups[:0]
	pruned := 0
	for _, b := range backups {
		if b.Status.Phase == v1alpha1.BackupPhaseCompleted {
			reasons, ok := keep[b.Name]
			if !ok {
				doomed[b.Name] = true
			} else if !slices.Equal(b.Status.RetainedBy, reasons) {
				orig := b.DeepCopy()
				b.Status.RetainedBy = reasons
				if _, err := patchStatus(ctx, r.Client, &b, orig); client.IgnoreNotFound(err) != nil {
					return nil, err
				}
			}
		}
		if doomed[b.Name] {
			if err := r.Delete(ctx, &b); client.IgnoreNotFound(err) != nil {
				return nil, fmt.Errorf("prune backup %s: %w", b.Name, err)
			}
			pruned++
			continue
		}
		remaining = append(remaining, b)
	}
	if pruned > 0 {
		prunedTotal.WithLabelValues(cfg.Namespace, cfg.Name).Add(float64(pruned))
		r.Recorder.Eventf(cfg, nil, corev1.EventTypeNormal, "Pruned", "Retention", "Deleted %d backup(s) not kept by the retention policy", pruned)
	}
	return remaining, nil
}

func (r *BackupConfigReconciler) summarize(cfg *v1alpha1.BackupConfig, backups []v1alpha1.Backup, selected int) {
	st := &cfg.Status
	st.SelectedPVCs = int32(selected) //nolint:gosec // PVC counts fit in int32
	st.CompletedBackups, st.FailedBackups, st.ActiveBackups = 0, 0, 0
	var newestDone *v1alpha1.Backup
	newestPerPVC := map[string]*v1alpha1.Backup{}
	for i := range backups {
		b := &backups[i]
		switch b.Status.Phase {
		case v1alpha1.BackupPhaseCompleted:
			st.CompletedBackups++
			if n := newestPerPVC[b.Spec.PVCName]; n == nil || b.Status.CompletionTime.After(n.Status.CompletionTime.Time) {
				newestPerPVC[b.Spec.PVCName] = b
			}
		case v1alpha1.BackupPhaseFailed:
			st.FailedBackups++
		default:
			st.ActiveBackups++
			continue
		}
		if b.Status.CompletionTime != nil && (newestDone == nil || b.Status.CompletionTime.After(newestDone.Status.CompletionTime.Time)) {
			newestDone = b
		}
	}
	for pvc, b := range newestPerPVC {
		lastSuccess.WithLabelValues(cfg.Namespace, cfg.Name, pvc).Set(float64(b.Status.CompletionTime.Unix()))
		backupSize.WithLabelValues(cfg.Namespace, cfg.Name, pvc).Set(float64(b.Status.SizeBytes))
		if st.LastSuccessfulTime == nil || b.Status.CompletionTime.After(st.LastSuccessfulTime.Time) {
			st.LastSuccessfulTime = b.Status.CompletionTime.DeepCopy()
		}
	}
	if newestDone != nil {
		c := metav1.Condition{Type: "LastBackupSucceeded", Status: metav1.ConditionTrue, Reason: "Completed",
			Message: fmt.Sprintf("backup %s completed", newestDone.Name), ObservedGeneration: cfg.Generation}
		if newestDone.Status.Phase == v1alpha1.BackupPhaseFailed {
			c.Status, c.Reason = metav1.ConditionFalse, "Failed"
			c.Message = fmt.Sprintf("backup %s failed: %s", newestDone.Name, newestDone.Status.Message)
		}
		meta.SetStatusCondition(&st.Conditions, c)
	}
}

// SetupWithManager registers the controller.
func (r *BackupConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.BackupConfig{}, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.AnnotationChangedPredicate{}))).
		Watches(&v1alpha1.Backup{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			name := o.GetLabels()[v1alpha1.LabelBackupConfig]
			if name == "" {
				return nil
			}
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: name}}}
		})).
		Named("backupconfig").
		Complete(r)
}
