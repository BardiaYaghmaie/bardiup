package controller

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/BardiaYaghmaie/bardiup/api/v1alpha1"
	"github.com/BardiaYaghmaie/bardiup/internal/agent"
	"github.com/BardiaYaghmaie/bardiup/internal/storage"
)

const ns = "app"

var t0 = time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

type fakeStore struct {
	checkErr error
	deleted  []string
}

func (f *fakeStore) Check(context.Context) error { return f.checkErr }
func (f *fakeStore) Delete(_ context.Context, keys ...string) error {
	f.deleted = append(f.deleted, keys...)
	return nil
}

type env struct {
	c     client.Client
	clock time.Time
	store *fakeStore
	cfgR  *BackupConfigReconciler
	bkR   *BackupReconciler
	rsR   *RestoreReconciler
}

func newEnv(t *testing.T, objs ...client.Object) *env {
	t.Helper()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.BackupConfig{}, &v1alpha1.Backup{}, &v1alpha1.Restore{}, &batchv1.Job{}, &corev1.Pod{}).
		Build()
	e := &env{c: c, clock: t0, store: &fakeStore{}}
	now := func() time.Time { return e.clock }
	stores := func(context.Context, v1alpha1.S3Storage, storage.Credentials) (ObjectStore, error) {
		return e.store, nil
	}
	rec := events.NewFakeRecorder(1000)
	jobs := JobSettings{Image: "ghcr.io/bardiayaghmaie/bardiup:test", ImagePullPolicy: corev1.PullIfNotPresent}
	e.cfgR = &BackupConfigReconciler{Client: c, APIReader: c, Recorder: rec, Stores: stores, Now: now}
	e.bkR = &BackupReconciler{Client: c, APIReader: c, Recorder: rec, Jobs: jobs, Stores: stores, Now: now}
	e.rsR = &RestoreReconciler{Client: c, APIReader: c, Recorder: rec, Jobs: jobs, Now: now}
	return e
}

func req(name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}
}

func pvc(name string, modes ...corev1.PersistentVolumeAccessMode) *corev1.PersistentVolumeClaim {
	if len(modes) == 0 {
		modes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	}
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"app": "db"}},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      modes,
			StorageClassName: ptr.To("standard"),
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}},
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Phase:    corev1.ClaimBound,
			Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
		},
	}
}

func secret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "s3", Namespace: ns},
		Data:       map[string][]byte{KeyAccessKeyID: []byte("a"), KeySecretAccessKey: []byte("s")},
	}
}

func storageSpec() v1alpha1.StorageSpec {
	return v1alpha1.StorageSpec{S3: v1alpha1.S3Storage{
		Bucket: "bk", Prefix: "team", CredentialsSecretRef: &corev1.LocalObjectReference{Name: "s3"},
	}}
}

func config(mut ...func(*v1alpha1.BackupConfig)) *v1alpha1.BackupConfig {
	c := &v1alpha1.BackupConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: ns, Generation: 1, CreationTimestamp: metav1.NewTime(t0.Add(-time.Hour))},
		Spec: v1alpha1.BackupConfigSpec{
			Schedule:    "0 2 * * *",
			PVCSelector: v1alpha1.PVCSelector{MatchLabels: map[string]string{"app": "db"}},
			Storage:     storageSpec(),
			Retention:   v1alpha1.RetentionPolicy{Daily: ptr.To[int32](2)},
		},
	}
	for _, m := range mut {
		m(c)
	}
	return c
}

func (e *env) getConfig(t *testing.T) *v1alpha1.BackupConfig {
	t.Helper()
	c := &v1alpha1.BackupConfig{}
	if err := e.c.Get(context.Background(), req("nightly").NamespacedName, c); err != nil {
		t.Fatal(err)
	}
	return c
}

func (e *env) backups(t *testing.T) []v1alpha1.Backup {
	t.Helper()
	l := &v1alpha1.BackupList{}
	if err := e.c.List(context.Background(), l, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	return l.Items
}

func (e *env) run(t *testing.T, r reconcile.Reconciler, name string) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), req(name)); err != nil {
		t.Fatal(err)
	}
}

// --- BackupConfig ---

func TestConfigInvalidSpec(t *testing.T) {
	for name, mut := range map[string]func(*v1alpha1.BackupConfig){
		"cron":     func(c *v1alpha1.BackupConfig) { c.Spec.Schedule = "every day" },
		"timezone": func(c *v1alpha1.BackupConfig) { c.Spec.TimeZone = "Mars/Olympus" },
		"retention": func(c *v1alpha1.BackupConfig) {
			c.Spec.Retention.Custom = []v1alpha1.CustomRetention{{Every: "5m", Keep: 1}}
		},
		"exclude": func(c *v1alpha1.BackupConfig) { c.Spec.Excludes = []string{"[unclosed"} },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, config(mut), secret(), pvc("data"))
			e.clock = t0.Add(48 * time.Hour)
			res, err := e.cfgR.Reconcile(context.Background(), req("nightly"))
			if err != nil {
				t.Fatal(err)
			}
			c := e.getConfig(t)
			cond := meta.FindStatusCondition(c.Status.Conditions, "Ready")
			if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "InvalidSpec" {
				t.Fatalf("Ready = %+v", cond)
			}
			if len(e.backups(t)) != 0 || res.RequeueAfter != 0 {
				t.Fatal("invalid config must not create backups or requeue")
			}
		})
	}
}

func TestConfigSchedulesOnlyEligiblePVCs(t *testing.T) {
	unbound := pvc("unbound")
	unbound.Status.Phase = corev1.ClaimPending
	managed := pvc("clone")
	managed.Labels[v1alpha1.LabelManagedBy] = v1alpha1.ManagedByValue
	other := pvc("other")
	other.Labels = map[string]string{"app": "web"}
	e := newEnv(t, config(), secret(), pvc("data-0"), pvc("data-1"), unbound, managed, other)

	// Before 02:00 nothing happens.
	e.run(t, e.cfgR, "nightly")
	if n := len(e.backups(t)); n != 0 {
		t.Fatalf("created %d backups before schedule", n)
	}
	c := e.getConfig(t)
	if !meta.IsStatusConditionTrue(c.Status.Conditions, "Ready") {
		t.Fatalf("not ready: %+v", c.Status.Conditions)
	}
	if want := time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC); !c.Status.NextScheduleTime.Time.Equal(want) {
		t.Fatalf("next = %v want %v", c.Status.NextScheduleTime, want)
	}

	e.clock = time.Date(2026, 11, 20, 2, 0, 5, 0, time.UTC)
	res, err := e.cfgR.Reconcile(context.Background(), req("nightly"))
	if err != nil {
		t.Fatal(err)
	}
	bs := e.backups(t)
	var names []string
	for _, b := range bs {
		names = append(names, b.Name)
		if b.Spec.Storage.S3.Bucket != "bk" || b.Labels[v1alpha1.LabelBackupConfig] != "nightly" || b.Spec.DeletionPolicy != v1alpha1.DeletionPolicyDelete {
			t.Errorf("backup %s not populated from config: %+v", b.Name, b)
		}
	}
	slices.Sort(names)
	want := []string{"nightly-data-0-20261120-020000", "nightly-data-1-20261120-020000"}
	if !slices.Equal(names, want) {
		t.Fatalf("backups %v want %v", names, want)
	}
	c = e.getConfig(t)
	if c.Status.SelectedPVCs != 2 || c.Status.ActiveBackups != 2 {
		t.Fatalf("status %+v", c.Status)
	}
	if res.RequeueAfter <= 23*time.Hour || res.RequeueAfter > 24*time.Hour {
		t.Fatalf("requeue after %v", res.RequeueAfter)
	}

	// Running backups block a new run for the same PVC.
	e.clock = e.clock.Add(24 * time.Hour)
	e.run(t, e.cfgR, "nightly")
	if n := len(e.backups(t)); n != 2 {
		t.Fatalf("expected no new backups while previous are active, have %d", n)
	}
}

func TestConfigRunIsIdempotent(t *testing.T) {
	e := newEnv(t, config(), secret(), pvc("data"))
	e.clock = t0.Add(20 * time.Hour)
	e.run(t, e.cfgR, "nightly")
	// Simulate a lost status write: reset lastScheduleTime and reconcile again.
	c := e.getConfig(t)
	c.Status.LastScheduleTime = nil
	if err := e.c.Status().Update(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	for _, b := range e.backups(t) {
		b.Status.Phase = v1alpha1.BackupPhaseFailed
		if err := e.c.Status().Update(context.Background(), &b); err != nil {
			t.Fatal(err)
		}
	}
	e.run(t, e.cfgR, "nightly")
	if n := len(e.backups(t)); n != 1 {
		t.Fatalf("got %d backups, want 1", n)
	}
}

func TestConfigBackupNowAndSuspend(t *testing.T) {
	e := newEnv(t, config(func(c *v1alpha1.BackupConfig) {
		c.Spec.Suspend = true
		c.Annotations = map[string]string{v1alpha1.AnnotationBackupNow: "1"}
	}), secret(), pvc("data"))
	e.clock = t0.Add(72 * time.Hour)
	res, err := e.cfgR.Reconcile(context.Background(), req("nightly"))
	if err != nil {
		t.Fatal(err)
	}
	bs := e.backups(t)
	if len(bs) != 1 || !strings.Contains(bs[0].Name, "-m") {
		t.Fatalf("expected one manual backup, got %v", bs)
	}
	c := e.getConfig(t)
	if c.Status.LastBackupNowTrigger != "1" || c.Status.NextScheduleTime != nil || res.RequeueAfter != 0 {
		t.Fatalf("status %+v requeue %v", c.Status, res.RequeueAfter)
	}
	// Same trigger value again does nothing.
	e.run(t, e.cfgR, "nightly")
	if len(e.backups(t)) != 1 {
		t.Fatal("trigger processed twice")
	}
}

func TestConfigStorageUnavailable(t *testing.T) {
	e := newEnv(t, config(), secret(), pvc("data"))
	e.store.checkErr = errors.New("403 Forbidden")
	res, err := e.cfgR.Reconcile(context.Background(), req("nightly"))
	if err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(e.getConfig(t).Status.Conditions, "Ready")
	if cond.Status != metav1.ConditionFalse || cond.Reason != "StorageUnavailable" || !strings.Contains(cond.Message, "403") {
		t.Fatalf("Ready = %+v", cond)
	}
	if res.RequeueAfter != time.Minute {
		t.Fatalf("requeue %v", res.RequeueAfter)
	}
	// Recovers once storage works.
	e.store.checkErr = nil
	e.run(t, e.cfgR, "nightly")
	if !meta.IsStatusConditionTrue(e.getConfig(t).Status.Conditions, "Ready") {
		t.Fatal("did not recover")
	}
}

func TestConfigMissingSecret(t *testing.T) {
	e := newEnv(t, config(), pvc("data"))
	e.run(t, e.cfgR, "nightly")
	cond := meta.FindStatusCondition(e.getConfig(t).Status.Conditions, "Ready")
	if cond.Status != metav1.ConditionFalse || !strings.Contains(cond.Message, "not found") {
		t.Fatalf("Ready = %+v", cond)
	}
}

func finished(name, pvcName string, phase v1alpha1.BackupPhase, created time.Time) *v1alpha1.Backup {
	return &v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(created),
			Labels: map[string]string{v1alpha1.LabelBackupConfig: "nightly"},
		},
		Spec: v1alpha1.BackupSpec{PVCName: pvcName, Storage: storageSpec()},
		Status: v1alpha1.BackupStatus{
			Phase: phase, CompletionTime: &metav1.Time{Time: created.Add(time.Minute)}, SizeBytes: 100,
		},
	}
}

func TestConfigRetentionPerPVC(t *testing.T) {
	objs := []client.Object{config(func(c *v1alpha1.BackupConfig) {
		c.Spec.FailedBackupsHistoryLimit = ptr.To[int32](1)
	}), secret(), pvc("a"), pvc("b")}
	for d := range 4 {
		day := t0.AddDate(0, 0, -d)
		objs = append(objs,
			finished("a-"+day.Format("0102"), "a", v1alpha1.BackupPhaseCompleted, day),
			finished("b-"+day.Format("0102"), "b", v1alpha1.BackupPhaseCompleted, day))
	}
	objs = append(objs,
		finished("a-failed-1", "a", v1alpha1.BackupPhaseFailed, t0.Add(-time.Hour)),
		finished("a-failed-2", "a", v1alpha1.BackupPhaseFailed, t0.Add(-2*time.Hour)),
		finished("manual", "a", v1alpha1.BackupPhaseCompleted, t0.AddDate(-1, 0, 0)))
	// "manual" is not labelled with the config: never pruned.
	objs[len(objs)-1].SetLabels(nil)

	e := newEnv(t, objs...)
	e.run(t, e.cfgR, "nightly")

	var left []string
	for _, b := range e.backups(t) {
		left = append(left, b.Name)
		if b.Status.Phase == v1alpha1.BackupPhaseCompleted && b.Name != "manual" && !slices.Equal(b.Status.RetainedBy, []string{"daily"}) {
			t.Errorf("%s retainedBy = %v", b.Name, b.Status.RetainedBy)
		}
	}
	slices.Sort(left)
	want := []string{"a-1118", "a-1119", "a-failed-1", "b-1118", "b-1119", "manual"}
	if !slices.Equal(left, want) {
		t.Fatalf("remaining %v want %v", left, want)
	}
	c := e.getConfig(t)
	if c.Status.CompletedBackups != 4 || c.Status.FailedBackups != 1 {
		t.Fatalf("status counts %+v", c.Status)
	}
	if c.Status.LastSuccessfulTime == nil || !c.Status.LastSuccessfulTime.Time.Equal(t0.Add(time.Minute)) {
		t.Fatalf("lastSuccessfulTime %v", c.Status.LastSuccessfulTime)
	}
	cond := meta.FindStatusCondition(c.Status.Conditions, "LastBackupSucceeded")
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("LastBackupSucceeded = %+v", cond)
	}
}

func TestConfigNoRetentionKeepsAll(t *testing.T) {
	objs := []client.Object{config(func(c *v1alpha1.BackupConfig) { c.Spec.Retention = v1alpha1.RetentionPolicy{} }), secret(), pvc("a")}
	for d := range 10 {
		objs = append(objs, finished("a-"+time.Duration(d).String(), "a", v1alpha1.BackupPhaseCompleted, t0.AddDate(0, 0, -d)))
	}
	e := newEnv(t, objs...)
	e.run(t, e.cfgR, "nightly")
	if n := len(e.backups(t)); n != 10 {
		t.Fatalf("kept %d, want 10", n)
	}
}

// --- Backup ---

func newBackup(mut ...func(*v1alpha1.Backup)) *v1alpha1.Backup {
	b := &v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "bk1", Namespace: ns, UID: "uid-1", Labels: map[string]string{v1alpha1.LabelBackupConfig: "nightly"}},
		Spec:       v1alpha1.BackupSpec{PVCName: "data", Storage: storageSpec(), Excludes: []string{"*.tmp"}},
	}
	for _, m := range mut {
		m(b)
	}
	return b
}

func (e *env) backup(t *testing.T) *v1alpha1.Backup {
	t.Helper()
	b := &v1alpha1.Backup{}
	if err := e.c.Get(context.Background(), req("bk1").NamespacedName, b); err != nil {
		t.Fatal(err)
	}
	return b
}

func (e *env) job(t *testing.T, name string) *batchv1.Job {
	t.Helper()
	j := &batchv1.Job{}
	if err := e.c.Get(context.Background(), req(name).NamespacedName, j); err != nil {
		t.Fatal(err)
	}
	return j
}

// finishJob marks a Job finished and creates its pod with the agent's termination message.
func (e *env) finishJob(t *testing.T, name string, ok bool, res agent.Result) {
	t.Helper()
	j := e.job(t, name)
	cond := batchv1.JobComplete
	if !ok {
		cond = batchv1.JobFailed
	}
	j.Status.Conditions = append(j.Status.Conditions, batchv1.JobCondition{Type: cond, Status: corev1.ConditionTrue, Message: "BackoffLimitExceeded"})
	if err := e.c.Status().Update(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	msg, _ := json.Marshal(res)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-x", Namespace: ns, Labels: map[string]string{batchv1.JobNameLabel: name}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: string(msg), FinishedAt: metav1.Now()}},
		}}},
	}
	if err := e.c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
}

func TestBackupCopyLifecycle(t *testing.T) {
	e := newEnv(t, newBackup(), pvc("data"), secret())
	ctx := context.Background()

	e.run(t, e.bkR, "bk1")
	b := e.backup(t)
	if b.Status.Phase != v1alpha1.BackupPhasePending || b.Status.ObjectKey != "team/app/data/bk1.tar.gz" ||
		b.Status.Source.Capacity != "10Gi" || *b.Status.Source.StorageClassName != "standard" {
		t.Fatalf("after init: %+v", b.Status)
	}
	if !slices.Contains(b.Finalizers, v1alpha1.Finalizer) {
		t.Fatal("finalizer missing")
	}

	e.run(t, e.bkR, "bk1")
	b = e.backup(t)
	if b.Status.Phase != v1alpha1.BackupPhaseRunning || b.Status.JobName != "backup-bk1" {
		t.Fatalf("after start: %+v", b.Status)
	}
	j := e.job(t, "backup-bk1")
	pod := j.Spec.Template.Spec
	ctr := pod.Containers[0]
	if !pod.Volumes[0].PersistentVolumeClaim.ReadOnly || !ctr.VolumeMounts[0].ReadOnly {
		t.Error("backup must mount the volume read-only")
	}
	if ctr.EnvFrom[0].SecretRef.Name != "s3" || !slices.Equal(ctr.Args, []string{"agent", "backup"}) {
		t.Errorf("container %+v", ctr)
	}
	if !slices.Equal(ctr.SecurityContext.Capabilities.Add, []corev1.Capability{"DAC_OVERRIDE"}) || *ctr.SecurityContext.AllowPrivilegeEscalation {
		t.Errorf("security context %+v", ctr.SecurityContext)
	}
	if pod.Affinity != nil || *pod.AutomountServiceAccountToken {
		t.Errorf("unexpected affinity or token automount: %+v", pod)
	}
	var cfg agent.Config
	if err := json.Unmarshal([]byte(ctr.Env[0].Value), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ObjectKey != b.Status.ObjectKey || cfg.SizeHint != 10<<30 || cfg.Manifest.PVC != "data" || cfg.Excludes[0] != "*.tmp" {
		t.Fatalf("agent config %+v", cfg)
	}
	if j.OwnerReferences[0].UID != "uid-1" || *j.Spec.BackoffLimit != 2 || *j.Spec.ActiveDeadlineSeconds != 21600 {
		t.Fatalf("job meta %+v %+v", j.OwnerReferences, j.Spec)
	}

	// Still running: no change.
	e.run(t, e.bkR, "bk1")
	if e.backup(t).Status.Phase != v1alpha1.BackupPhaseRunning {
		t.Fatal("phase changed while job running")
	}

	e.finishJob(t, "backup-bk1", true, agent.Result{SHA256: "abc", SizeBytes: 2048, Files: 3, DataBytes: 4096, Warnings: []string{"x changed"}})
	e.clock = t0.Add(5 * time.Minute)
	e.run(t, e.bkR, "bk1")
	b = e.backup(t)
	if b.Status.Phase != v1alpha1.BackupPhaseCompleted || b.Status.SHA256 != "abc" || b.Status.Size != "2.0 KiB" || b.Status.Files != 3 {
		t.Fatalf("after completion: %+v", b.Status)
	}
	if !strings.Contains(b.Status.Message, "1 warning") {
		t.Errorf("message %q", b.Status.Message)
	}
	if err := e.c.Get(ctx, req("backup-bk1").NamespacedName, &batchv1.Job{}); err == nil {
		t.Error("finished job should be deleted")
	}

	// Deletion removes archive and manifest from storage.
	if err := e.c.Delete(ctx, b); err != nil {
		t.Fatal(err)
	}
	e.run(t, e.bkR, "bk1")
	if !slices.Equal(e.store.deleted, []string{"team/app/data/bk1.tar.gz", "team/app/data/bk1.tar.gz.json"}) {
		t.Fatalf("deleted %v", e.store.deleted)
	}
	if err := e.c.Get(ctx, req("bk1").NamespacedName, &v1alpha1.Backup{}); err == nil {
		t.Fatal("backup should be gone after finalizer removal")
	}
}

func TestBackupRetainPolicyKeepsData(t *testing.T) {
	b := newBackup(func(b *v1alpha1.Backup) {
		b.Spec.DeletionPolicy = v1alpha1.DeletionPolicyRetain
		b.Finalizers = []string{v1alpha1.Finalizer}
		b.Status = v1alpha1.BackupStatus{Phase: v1alpha1.BackupPhaseCompleted, ObjectKey: "k"}
	})
	e := newEnv(t, b, secret())
	if err := e.c.Delete(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	e.run(t, e.bkR, "bk1")
	if len(e.store.deleted) != 0 {
		t.Fatalf("deleted %v despite Retain", e.store.deleted)
	}
}

func TestBackupJobFailure(t *testing.T) {
	e := newEnv(t, newBackup(), pvc("data"), secret())
	for range 2 {
		e.run(t, e.bkR, "bk1")
	}
	e.finishJob(t, "backup-bk1", false, agent.Result{Error: "upload: AccessDenied"})
	e.run(t, e.bkR, "bk1")
	b := e.backup(t)
	if b.Status.Phase != v1alpha1.BackupPhaseFailed || !strings.Contains(b.Status.Message, "AccessDenied") {
		t.Fatalf("status %+v", b.Status)
	}
	// The failed job is kept for its logs.
	e.job(t, "backup-bk1")
}

func TestBackupFailsWhenNodeIsLost(t *testing.T) {
	e := newEnv(t, newBackup(), pvc("data"), secret())
	for range 2 {
		e.run(t, e.bkR, "bk1")
	}
	j := e.job(t, "backup-bk1")
	j.Status.Conditions = append(j.Status.Conditions, batchv1.JobCondition{
		Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Message: "Job was active longer than specified deadline"})
	if err := e.c.Status().Update(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	e.run(t, e.bkR, "bk1")
	if b := e.backup(t); b.Status.Phase != v1alpha1.BackupPhaseFailed || !strings.Contains(b.Status.Message, "deadline") {
		t.Fatalf("status %+v", b.Status)
	}
}

func TestBackupFailsFast(t *testing.T) {
	block := pvc("data")
	block.Spec.VolumeMode = ptr.To(corev1.PersistentVolumeBlock)
	cases := map[string]struct {
		objs []client.Object
		want string
	}{
		"missing pvc":    {[]client.Object{secret()}, "not found"},
		"missing secret": {[]client.Object{pvc("data")}, "secret \"s3\" not found"},
		"block volume":   {[]client.Object{block, secret()}, "block-mode"},
		"bad secret": {[]client.Object{pvc("data"), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s3", Namespace: ns}}},
			"must contain"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, append(tc.objs, newBackup())...)
			e.run(t, e.bkR, "bk1")
			b := e.backup(t)
			if b.Status.Phase != v1alpha1.BackupPhaseFailed || !strings.Contains(b.Status.Message, tc.want) {
				t.Fatalf("status %+v", b.Status)
			}
		})
	}
}

func TestPodSecurityFailFast(t *testing.T) {
	restricted := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: map[string]string{"pod-security.kubernetes.io/enforce": "restricted"}}}
	e := newEnv(t, restricted, newBackup(), pvc("data"), secret())
	e.run(t, e.bkR, "bk1")
	if b := e.backup(t); b.Status.Phase != v1alpha1.BackupPhaseFailed || !strings.Contains(b.Status.Message, "spec.job.securityContext") {
		t.Fatalf("status %+v", b.Status)
	}
	// A custom security context opts out of the check.
	e = newEnv(t, restricted, newBackup(func(b *v1alpha1.Backup) {
		b.Spec.Job.SecurityContext = &corev1.SecurityContext{RunAsNonRoot: ptr.To(true)}
	}), pvc("data"), secret())
	e.run(t, e.bkR, "bk1")
	if b := e.backup(t); b.Status.Phase != v1alpha1.BackupPhasePending {
		t.Fatalf("status %+v", b.Status)
	}
}

func appPod(name, claim, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: corev1.PodSpec{NodeName: node, Volumes: []corev1.Volume{{Name: "v", VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func TestBackupPinsToWorkloadNode(t *testing.T) {
	e := newEnv(t, newBackup(), pvc("data"), secret(), appPod("db-0", "data", "node-7"), appPod("other", "x", "node-1"))
	for range 2 {
		e.run(t, e.bkR, "bk1")
	}
	pod := e.job(t, "backup-bk1").Spec.Template.Spec
	terms := pod.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if terms[0].MatchExpressions[0].Values[0] != "node-7" {
		t.Fatalf("affinity %+v", terms)
	}
	if pod.Tolerations[len(pod.Tolerations)-1].Operator != corev1.TolerationOpExists {
		t.Fatal("pinned pod must tolerate the node's taints")
	}
	if !strings.Contains(e.backup(t).Status.Message, "node-7") {
		t.Error("message should mention node")
	}
}

func TestBackupNoPinningForRWX(t *testing.T) {
	e := newEnv(t, newBackup(), pvc("data", corev1.ReadWriteMany), secret(), appPod("web", "data", "node-7"))
	for range 2 {
		e.run(t, e.bkR, "bk1")
	}
	if e.job(t, "backup-bk1").Spec.Template.Spec.Affinity != nil {
		t.Fatal("RWX volumes need no pinning")
	}
}

func TestBackupRWOPInUse(t *testing.T) {
	e := newEnv(t, newBackup(), pvc("data", corev1.ReadWriteOncePod), secret(), appPod("db", "data", "n"))
	for range 2 {
		e.run(t, e.bkR, "bk1")
	}
	if b := e.backup(t); b.Status.Phase != v1alpha1.BackupPhaseFailed || !strings.Contains(b.Status.Message, "snapshot") {
		t.Fatalf("status %+v", b.Status)
	}
}

func TestBackupCustomJobOptions(t *testing.T) {
	sc := &corev1.SecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To[int64](1000)}
	e := newEnv(t, newBackup(func(b *v1alpha1.Backup) {
		b.Spec.Job = v1alpha1.JobOptions{SecurityContext: sc, ServiceAccountName: "irsa", BackoffLimit: ptr.To[int32](0),
			NodeSelector: map[string]string{"pool": "backup"}}
		b.Spec.Storage.S3.CredentialsSecretRef = nil
	}), pvc("data"))
	for range 2 {
		e.run(t, e.bkR, "bk1")
	}
	j := e.job(t, "backup-bk1")
	pod := j.Spec.Template.Spec
	if *pod.Containers[0].SecurityContext.RunAsUser != 1000 || pod.ServiceAccountName != "irsa" || pod.AutomountServiceAccountToken != nil ||
		*j.Spec.BackoffLimit != 0 || pod.NodeSelector["pool"] != "backup" || pod.Containers[0].EnvFrom != nil {
		t.Fatalf("job options not applied: %+v", pod)
	}
}

// --- Restore ---

func completedBackup() *v1alpha1.Backup {
	return newBackup(func(b *v1alpha1.Backup) {
		b.Status = v1alpha1.BackupStatus{
			Phase: v1alpha1.BackupPhaseCompleted, ObjectKey: "team/app/data/bk1.tar.gz", SHA256: "abc",
			Source: &v1alpha1.SourceVolume{StorageClassName: ptr.To("fast"), Capacity: "5Gi",
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}},
		}
	})
}

func restore(mut ...func(*v1alpha1.Restore)) *v1alpha1.Restore {
	r := &v1alpha1.Restore{
		ObjectMeta: metav1.ObjectMeta{Name: "rs1", Namespace: ns, UID: "uid-r"},
		Spec:       v1alpha1.RestoreSpec{BackupName: "bk1", Target: v1alpha1.RestoreTarget{PVCName: "data-restored", CleanTarget: true}},
	}
	for _, m := range mut {
		m(r)
	}
	return r
}

func (e *env) restore(t *testing.T) *v1alpha1.Restore {
	t.Helper()
	r := &v1alpha1.Restore{}
	if err := e.c.Get(context.Background(), req("rs1").NamespacedName, r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRestoreIntoNewPVC(t *testing.T) {
	e := newEnv(t, completedBackup(), restore(), secret())
	ctx := context.Background()
	e.run(t, e.rsR, "rs1")

	p := &corev1.PersistentVolumeClaim{}
	if err := e.c.Get(ctx, req("data-restored").NamespacedName, p); err != nil {
		t.Fatal(err)
	}
	if *p.Spec.StorageClassName != "fast" || p.Spec.Resources.Requests.Storage().String() != "5Gi" || p.Annotations[AnnotationRestoredFrom] != "bk1" {
		t.Fatalf("pvc %+v", p)
	}
	r := e.restore(t)
	if r.Status.Phase != v1alpha1.RestorePhaseRunning || !r.Status.PVCCreated {
		t.Fatalf("status %+v", r.Status)
	}
	ctr := e.job(t, "restore-rs1").Spec.Template.Spec.Containers[0]
	var cfg agent.Config
	_ = json.Unmarshal([]byte(ctr.Env[0].Value), &cfg)
	if cfg.ExpectedSHA256 != "abc" || !cfg.Clean || cfg.ObjectKey != "team/app/data/bk1.tar.gz" || ctr.VolumeMounts[0].ReadOnly {
		t.Fatalf("restore job %+v / %+v", cfg, ctr)
	}
	if !slices.Contains(ctr.SecurityContext.Capabilities.Add, "CHOWN") {
		t.Error("restore needs CHOWN")
	}

	e.finishJob(t, "restore-rs1", true, agent.Result{SHA256: "abc", Files: 9})
	e.run(t, e.rsR, "rs1")
	r = e.restore(t)
	if r.Status.Phase != v1alpha1.RestorePhaseCompleted || r.Status.Files != 9 {
		t.Fatalf("status %+v", r.Status)
	}
	// Terminal restores are left alone.
	e.run(t, e.rsR, "rs1")
}

func TestRestoreWaitsForBackup(t *testing.T) {
	b := completedBackup()
	b.Status.Phase = v1alpha1.BackupPhaseRunning
	e := newEnv(t, b, restore(), secret())
	res, err := e.rsR.Reconcile(context.Background(), req("rs1"))
	if err != nil {
		t.Fatal(err)
	}
	if e.restore(t).Status.Phase != v1alpha1.RestorePhasePending || res.RequeueAfter == 0 {
		t.Fatalf("status %+v", e.restore(t).Status)
	}
}

func TestRestoreFromSourceRequiresSize(t *testing.T) {
	src := func(r *v1alpha1.Restore) {
		r.Spec.BackupName = ""
		r.Spec.Source = &v1alpha1.RestoreSource{Storage: storageSpec(), ObjectKey: "old/cluster/key.tar.gz"}
	}
	e := newEnv(t, restore(src), secret())
	e.run(t, e.rsR, "rs1")
	if r := e.restore(t); r.Status.Phase != v1alpha1.RestorePhaseFailed || !strings.Contains(r.Status.Message, "spec.target.size") {
		t.Fatalf("status %+v", r.Status)
	}

	e = newEnv(t, restore(src, func(r *v1alpha1.Restore) { r.Spec.Target.Size = ptr.To(resource.MustParse("1Gi")) }), secret())
	e.run(t, e.rsR, "rs1")
	if r := e.restore(t); r.Status.Phase != v1alpha1.RestorePhaseRunning {
		t.Fatalf("status %+v", r.Status)
	}
	var cfg agent.Config
	_ = json.Unmarshal([]byte(e.job(t, "restore-rs1").Spec.Template.Spec.Containers[0].Env[0].Value), &cfg)
	if cfg.ObjectKey != "old/cluster/key.tar.gz" || cfg.ExpectedSHA256 != "" {
		t.Fatalf("cfg %+v", cfg)
	}
}

func TestRestoreFailures(t *testing.T) {
	failedBackup := completedBackup()
	failedBackup.Status.Phase = v1alpha1.BackupPhaseFailed
	cases := map[string]struct {
		objs []client.Object
		want string
	}{
		"missing backup": {[]client.Object{restore(), secret()}, "not found"},
		"failed backup":  {[]client.Object{restore(), failedBackup, secret()}, "failed"},
		"missing secret": {[]client.Object{restore(), completedBackup()}, "secret"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, tc.objs...)
			e.run(t, e.rsR, "rs1")
			if r := e.restore(t); r.Status.Phase != v1alpha1.RestorePhaseFailed || !strings.Contains(r.Status.Message, tc.want) {
				t.Fatalf("status %+v", r.Status)
			}
		})
	}

	e := newEnv(t, completedBackup(), restore(), secret())
	e.run(t, e.rsR, "rs1")
	e.finishJob(t, "restore-rs1", false, agent.Result{Error: "checksum mismatch"})
	e.run(t, e.rsR, "rs1")
	if r := e.restore(t); r.Status.Phase != v1alpha1.RestorePhaseFailed || !strings.Contains(r.Status.Message, "checksum mismatch") {
		t.Fatalf("status %+v", r.Status)
	}
}

// --- helpers ---

func TestBoundedName(t *testing.T) {
	long := strings.Repeat("a", 80)
	got := boundedName("backup-"+long, 63)
	if len(got) != 63 || got == boundedName("backup-"+long+"b", 63) {
		t.Fatalf("bounded name %q not unique or wrong length", got)
	}
	if boundedName("short", 63) != "short" {
		t.Fatal("short names must be unchanged")
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 5 << 30: "5.0 GiB"} {
		if got := HumanBytes(n); got != want {
			t.Errorf("HumanBytes(%d) = %s want %s", n, got, want)
		}
	}
}

func TestBackupSnapshotLifecycle(t *testing.T) {
	e := newEnv(t, newBackup(func(b *v1alpha1.Backup) {
		b.Spec.Method = v1alpha1.BackupMethodSnapshot
		b.Spec.Snapshot = &v1alpha1.SnapshotOptions{VolumeSnapshotClassName: ptr.To("csi")}
	}), pvc("data"), secret(), appPod("db-0", "data", "node-7"))
	ctx := context.Background()
	e.run(t, e.bkR, "bk1") // init
	e.run(t, e.bkR, "bk1") // create snapshot
	b := e.backup(t)
	if b.Status.Phase != v1alpha1.BackupPhaseSnapshotting || b.Status.SnapshotName != "bk1-snap" {
		t.Fatalf("status %+v", b.Status)
	}
	vs := &unstructured.Unstructured{}
	vs.SetGroupVersionKind(volumeSnapshotGVK)
	if err := e.c.Get(ctx, req("bk1-snap").NamespacedName, vs); err != nil {
		t.Fatal(err)
	}
	if cls, _, _ := unstructured.NestedString(vs.Object, "spec", "volumeSnapshotClassName"); cls != "csi" {
		t.Fatalf("snapshot class %q", cls)
	}

	e.run(t, e.bkR, "bk1") // not ready yet
	if e.backup(t).Status.Phase != v1alpha1.BackupPhaseSnapshotting {
		t.Fatal("advanced before snapshot was ready")
	}
	_ = unstructured.SetNestedField(vs.Object, true, "status", "readyToUse")
	_ = unstructured.SetNestedField(vs.Object, "20Gi", "status", "restoreSize")
	if err := e.c.Update(ctx, vs); err != nil {
		t.Fatal(err)
	}
	e.run(t, e.bkR, "bk1")

	clone := &corev1.PersistentVolumeClaim{}
	if err := e.c.Get(ctx, req("bk1-clone").NamespacedName, clone); err != nil {
		t.Fatal(err)
	}
	if clone.Spec.DataSource.Name != "bk1-snap" || clone.Spec.Resources.Requests.Storage().String() != "20Gi" ||
		clone.Labels[v1alpha1.LabelManagedBy] != v1alpha1.ManagedByValue {
		t.Fatalf("clone %+v", clone.Spec)
	}
	pod := e.job(t, "backup-bk1").Spec.Template.Spec
	if pod.Volumes[0].PersistentVolumeClaim.ClaimName != "bk1-clone" || pod.Affinity != nil {
		t.Fatal("snapshot backups read the clone and need no pinning")
	}

	e.finishJob(t, "backup-bk1", true, agent.Result{SHA256: "abc"})
	e.run(t, e.bkR, "bk1")
	if e.backup(t).Status.Phase != v1alpha1.BackupPhaseCompleted {
		t.Fatal("not completed")
	}
	if err := e.c.Get(ctx, req("bk1-clone").NamespacedName, &corev1.PersistentVolumeClaim{}); err == nil {
		t.Error("clone PVC should be deleted")
	}
	if err := e.c.Get(ctx, req("bk1-snap").NamespacedName, vs); err == nil {
		t.Error("snapshot should be deleted")
	}
}

func TestBackupSnapshotError(t *testing.T) {
	e := newEnv(t, newBackup(func(b *v1alpha1.Backup) { b.Spec.Method = v1alpha1.BackupMethodSnapshot }), pvc("data"), secret())
	e.run(t, e.bkR, "bk1")
	e.run(t, e.bkR, "bk1")
	vs := &unstructured.Unstructured{}
	vs.SetGroupVersionKind(volumeSnapshotGVK)
	if err := e.c.Get(context.Background(), req("bk1-snap").NamespacedName, vs); err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedField(vs.Object, "driver does not support snapshots", "status", "error", "message")
	if err := e.c.Update(context.Background(), vs); err != nil {
		t.Fatal(err)
	}
	e.run(t, e.bkR, "bk1")
	if b := e.backup(t); b.Status.Phase != v1alpha1.BackupPhaseFailed || !strings.Contains(b.Status.Message, "does not support") {
		t.Fatalf("status %+v", b.Status)
	}
}

// staleClient serves one outdated Backup from Get, like an informer cache that hasn't caught up.
type staleClient struct {
	client.Client
	stale *v1alpha1.Backup
}

func (s *staleClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if b, ok := obj.(*v1alpha1.Backup); ok && s.stale != nil && key.Name == s.stale.Name {
		s.stale.DeepCopyInto(b)
		s.stale = nil
		return nil
	}
	return s.Client.Get(ctx, key, obj, opts...)
}

// Regression: a reconcile reading a stale "Running" Backup after completion (the Job is already
// deleted) must not overwrite the persisted Completed status.
func TestBackupStaleCacheCannotClobberCompletion(t *testing.T) {
	e := newEnv(t, newBackup(), pvc("data"), secret())
	e.run(t, e.bkR, "bk1")
	e.run(t, e.bkR, "bk1")
	stale := e.backup(t)
	if stale.Status.Phase != v1alpha1.BackupPhaseRunning {
		t.Fatalf("setup: %s", stale.Status.Phase)
	}
	e.finishJob(t, "backup-bk1", true, agent.Result{SHA256: "abc"})
	e.run(t, e.bkR, "bk1")
	if e.backup(t).Status.Phase != v1alpha1.BackupPhaseCompleted {
		t.Fatal("setup: not completed")
	}

	e.bkR.Client = &staleClient{Client: e.c, stale: stale}
	e.run(t, e.bkR, "bk1")
	if b := e.backup(t); b.Status.Phase != v1alpha1.BackupPhaseCompleted {
		t.Fatalf("stale reconcile overwrote status: %s %q", b.Status.Phase, b.Status.Message)
	}
}
