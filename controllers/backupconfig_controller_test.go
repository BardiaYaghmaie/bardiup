package controllers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bardiup/bardiup/api/v1alpha1"
	"github.com/bardiup/bardiup/pkg/retention"
	"github.com/bardiup/bardiup/pkg/storage"
	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/robfig/cron/v3"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestFindMatchingPVCs(t *testing.T) {
	ctx := context.Background()
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
			Labels: map[string]string{
				"app": "demo",
			},
		},
	}

	r := newTestReconciler(t, pvc)
	config := testBackupConfig()
	config.Spec.PVCSelector.Namespace = "default"
	config.Spec.PVCSelector.MatchLabels = map[string]string{"app": "demo"}

	pvcs, err := r.findMatchingPVCs(ctx, config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pvcs) != 1 || pvcs[0].Name != "test-pvc" {
		t.Fatalf("expected to find test-pvc, got %#v", pvcs)
	}
}

func TestCreateBackupCreatesResources(t *testing.T) {
	ctx := context.Background()
	pvc := newPVC("test-pvc", "default", map[string]string{"app": "demo"})
	r := newTestReconciler(t, pvc)
	config := testBackupConfig()
	config.Spec.PVCSelector = v1alpha1.PVCSelector{
		Namespace:   "default",
		MatchLabels: map[string]string{"app": "demo"},
	}

	fakeExec := newFakeExecutor(1)
	r.executors[config.Name] = fakeExec

	created, err := r.createBackup(ctx, config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created != 1 {
		t.Fatalf("expected 1 backup, got %d", created)
	}
	if !fakeExec.Wait(2 * time.Second) {
		t.Fatalf("expected executor to finish")
	}

	backupList := &v1alpha1.BackupList{}
	if err := r.List(ctx, backupList); err != nil {
		t.Fatalf("failed to list backups: %v", err)
	}
	if len(backupList.Items) != 1 {
		t.Fatalf("expected 1 backup resource, got %d", len(backupList.Items))
	}
	if backupList.Items[0].Spec.StorageLocation.Path == "" {
		t.Fatalf("expected storage path to be set")
	}
}

func TestScheduleBackupsSkipsWhenNoPVCs(t *testing.T) {
	ctx := context.Background()
	r := newTestReconciler(t)
	config := testBackupConfig()
	earlier := r.now().Add(-10 * time.Minute)
	config.Status.LastBackupTime = &metav1.Time{Time: earlier}

	if err := r.scheduleBackups(ctx, config); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !config.Status.LastBackupTime.Time.Equal(earlier) {
		t.Fatalf("expected last backup time to remain unchanged")
	}

	backupList := &v1alpha1.BackupList{}
	if err := r.List(ctx, backupList); err != nil {
		t.Fatalf("failed to list backups: %v", err)
	}
	if len(backupList.Items) != 0 {
		t.Fatalf("expected 0 backups, got %d", len(backupList.Items))
	}
}

func TestScheduleBackupsCreatesBackups(t *testing.T) {
	ctx := context.Background()
	pvc := newPVC("test-pvc", "default", map[string]string{"app": "demo"})
	r := newTestReconciler(t, pvc)
	config := testBackupConfig()
	config.Spec.PVCSelector = v1alpha1.PVCSelector{
		Namespace:   "default",
		MatchLabels: map[string]string{"app": "demo"},
	}
	config.Status.LastBackupTime = &metav1.Time{Time: r.now().Add(-10 * time.Minute)}

	fakeExec := newFakeExecutor(1)
	r.executors[config.Name] = fakeExec

	if err := r.scheduleBackups(ctx, config); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fakeExec.Wait(2 * time.Second) {
		t.Fatalf("expected executor to run")
	}
	if config.Status.BackupCount != 1 {
		t.Fatalf("expected backup count to be 1, got %d", config.Status.BackupCount)
	}
	if config.Status.LastBackupTime == nil || !config.Status.LastBackupTime.Time.Equal(r.now()) {
		t.Fatalf("expected last backup time to be updated")
	}
}

// Helpers

func newTestReconciler(t *testing.T, objs ...client.Object) *BackupConfigReconciler {
	t.Helper()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = v1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	logger := testr.New(t)
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	r := &BackupConfigReconciler{
		Client:    fakeClient,
		Scheme:    scheme,
		Log:       logger,
		scheduler: cron.New(),
		executors: make(map[string]BackupExecutor),
		retention: retention.NewManager(fakeClient, logger),
		lastRun:   make(map[string]time.Time),
		executorFactory: func(client.Client, storage.Backend, logr.Logger) BackupExecutor {
			return newFakeExecutor(0)
		},
		storageFactory: func(context.Context, client.Client, *v1alpha1.S3Backend, string, logr.Logger) (storage.Backend, error) {
			return nil, nil
		},
		now: func() time.Time { return now },
	}
	return r
}

func testBackupConfig() *v1alpha1.BackupConfig {
	return &v1alpha1.BackupConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-backup",
		},
		Spec: v1alpha1.BackupConfigSpec{
			Schedule: "*/1 * * * *",
			PVCSelector: v1alpha1.PVCSelector{
				Namespace: "default",
			},
			StorageBackend: v1alpha1.StorageBackend{
				Type:   "s3",
				Prefix: "bardiup",
				S3: &v1alpha1.S3Backend{
					Bucket:            "test",
					Region:            "us-west-2",
					CredentialsSecret: "creds",
				},
			},
			RetentionPolicy: v1alpha1.RetentionPolicy{
				Daily: &v1alpha1.RetentionRule{Keep: 7},
			},
		},
	}
}

func newPVC(name, namespace string, labels map[string]string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
	}
}

type fakeExecutor struct {
	wg            sync.WaitGroup
	executeCalls  int
	deleteCalls   int
	expectedCalls int
}

func newFakeExecutor(expected int) *fakeExecutor {
	fe := &fakeExecutor{expectedCalls: expected}
	if expected > 0 {
		fe.wg.Add(expected)
	}
	return fe
}

func (f *fakeExecutor) ExecuteBackup(context.Context, *v1alpha1.Backup, *v1alpha1.BackupConfig) error {
	f.executeCalls++
	if f.expectedCalls > 0 {
		f.wg.Done()
	}
	return nil
}

func (f *fakeExecutor) DeleteBackupData(context.Context, *v1alpha1.Backup) error {
	f.deleteCalls++
	return nil
}

func (f *fakeExecutor) Wait(timeout time.Duration) bool {
	if f.expectedCalls == 0 {
		return true
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.wg.Wait()
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}
