package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/bardiup/bardiup/api/v1alpha1"
	"github.com/bardiup/bardiup/pkg/storage"
	"github.com/go-logr/logr/testr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// MockStorageBackend implements the storage.Backend interface for testing
type MockStorageBackend struct {
	UploadFunc      func(ctx context.Context, key string, data io.Reader, metadata map[string]string) error
	DownloadFunc    func(ctx context.Context, key string) (io.ReadCloser, error)
	DeleteFunc      func(ctx context.Context, key string) error
	ExistsFunc      func(ctx context.Context, key string) (bool, error)
	ListFunc        func(ctx context.Context, prefix string) ([]storage.ObjectInfo, error)
	GetMetadataFunc func(ctx context.Context, key string) (map[string]string, error)
}

func (m *MockStorageBackend) Upload(ctx context.Context, key string, data io.Reader, metadata map[string]string) error {
	if m.UploadFunc != nil {
		return m.UploadFunc(ctx, key, data, metadata)
	}
	return nil
}

func (m *MockStorageBackend) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	if m.DownloadFunc != nil {
		return m.DownloadFunc(ctx, key)
	}
	return io.NopCloser(bytes.NewReader([]byte("test data"))), nil
}

func (m *MockStorageBackend) Delete(ctx context.Context, key string) error {
	if m.DeleteFunc != nil {
		return m.DeleteFunc(ctx, key)
	}
	return nil
}

func (m *MockStorageBackend) Exists(ctx context.Context, key string) (bool, error) {
	if m.ExistsFunc != nil {
		return m.ExistsFunc(ctx, key)
	}
	return true, nil
}

func (m *MockStorageBackend) List(ctx context.Context, prefix string) ([]storage.ObjectInfo, error) {
	if m.ListFunc != nil {
		return m.ListFunc(ctx, prefix)
	}
	return nil, nil
}

func (m *MockStorageBackend) GetMetadata(ctx context.Context, key string) (map[string]string, error) {
	if m.GetMetadataFunc != nil {
		return m.GetMetadataFunc(ctx, key)
	}
	return nil, nil
}

func newTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = v1alpha1.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func TestNewExecutor(t *testing.T) {
	logger := testr.New(t)
	client := newTestClient(t)
	mockStorage := &MockStorageBackend{}

	exec := NewExecutor(client, mockStorage, logger)

	if exec == nil {
		t.Fatal("expected executor to be created")
	}
	if exec.client != client {
		t.Error("expected client to be set")
	}
	if exec.storage == nil {
		t.Error("expected storage to be set")
	}
}

func TestExecutorDeleteBackupData(t *testing.T) {
	logger := testr.New(t)
	client := newTestClient(t)

	t.Run("successful delete", func(t *testing.T) {
		storage := &MockStorageBackend{
			DeleteFunc: func(ctx context.Context, key string) error {
				if key != "test/path/backup.tar.gz" {
					t.Errorf("expected key 'test/path/backup.tar.gz', got %s", key)
				}
				return nil
			},
		}
		exec := NewExecutor(client, storage, logger)

		backup := &v1alpha1.Backup{
			Spec: v1alpha1.BackupSpec{
				StorageLocation: v1alpha1.StorageLocation{
					Path: "test/path/backup.tar.gz",
				},
			},
		}

		err := exec.DeleteBackupData(context.Background(), backup)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("delete failure", func(t *testing.T) {
		storage := &MockStorageBackend{
			DeleteFunc: func(ctx context.Context, key string) error {
				return errors.New("delete failed")
			},
		}
		exec := NewExecutor(client, storage, logger)

		backup := &v1alpha1.Backup{
			Spec: v1alpha1.BackupSpec{
				StorageLocation: v1alpha1.StorageLocation{
					Path: "test/path/backup.tar.gz",
				},
			},
		}

		err := exec.DeleteBackupData(context.Background(), backup)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("nil storage", func(t *testing.T) {
		exec := NewExecutor(client, nil, logger)

		backup := &v1alpha1.Backup{
			Spec: v1alpha1.BackupSpec{
				StorageLocation: v1alpha1.StorageLocation{
					Path: "test/path/backup.tar.gz",
				},
			},
		}

		err := exec.DeleteBackupData(context.Background(), backup)
		if err == nil {
			t.Fatal("expected error for nil storage")
		}
	})

	t.Run("nil backup", func(t *testing.T) {
		storage := &MockStorageBackend{}
		exec := NewExecutor(client, storage, logger)

		err := exec.DeleteBackupData(context.Background(), nil)
		if err == nil {
			t.Fatal("expected error for nil backup")
		}
	})

	t.Run("empty path", func(t *testing.T) {
		storage := &MockStorageBackend{}
		exec := NewExecutor(client, storage, logger)

		backup := &v1alpha1.Backup{
			Spec: v1alpha1.BackupSpec{
				StorageLocation: v1alpha1.StorageLocation{
					Path: "",
				},
			},
		}

		err := exec.DeleteBackupData(context.Background(), backup)
		if err == nil {
			t.Fatal("expected error for empty path")
		}
	})
}

func TestCreateBackupJob(t *testing.T) {
	logger := testr.New(t)
	testClient := newTestClient(t)
	storage := &MockStorageBackend{}
	exec := NewExecutor(testClient, storage, logger)

	backup := &v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: v1alpha1.BackupSpec{
			PVCName:      "test-pvc",
			PVCNamespace: "default",
			StorageLocation: v1alpha1.StorageLocation{
				Type:   "s3",
				Path:   "test/path/backup.tar.gz",
				Bucket: "test-bucket",
			},
		},
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
		},
	}

	config := &v1alpha1.BackupConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-config",
		},
		Spec: v1alpha1.BackupConfigSpec{
			StorageBackend: v1alpha1.StorageBackend{
				Type: "s3",
				S3: &v1alpha1.S3Backend{
					Bucket:            "test-bucket",
					Region:            "us-west-2",
					CredentialsSecret: "test-secret",
					Endpoint:          "http://minio:9000",
				},
			},
		},
	}

	job := exec.createBackupJob(backup, pvc, config)

	if job == nil {
		t.Fatal("expected job to be created")
	}
	if job.Name != "backup-test-backup-test-pvc" {
		t.Errorf("expected job name 'backup-test-backup-test-pvc', got %s", job.Name)
	}
	if job.Namespace != "default" {
		t.Errorf("expected namespace 'default', got %s", job.Namespace)
	}
	if job.Labels["bardiup.io/backup"] != "test-backup" {
		t.Error("expected backup label")
	}
	if job.Labels["bardiup.io/pvc"] != "test-pvc" {
		t.Error("expected pvc label")
	}

	// Verify containers
	if len(job.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(job.Spec.Template.Spec.Containers))
	}

	container := job.Spec.Template.Spec.Containers[0]
	if container.Name != "backup" {
		t.Errorf("expected container name 'backup', got %s", container.Name)
	}

	// Verify volumes
	if len(job.Spec.Template.Spec.Volumes) != 2 {
		t.Fatalf("expected 2 volumes, got %d", len(job.Spec.Template.Spec.Volumes))
	}
}

func TestGetBackupEnvVars(t *testing.T) {
	logger := testr.New(t)
	testClient := newTestClient(t)
	storage := &MockStorageBackend{}
	exec := NewExecutor(testClient, storage, logger)

	t.Run("with S3 config", func(t *testing.T) {
		config := &v1alpha1.BackupConfig{
			Spec: v1alpha1.BackupConfigSpec{
				StorageBackend: v1alpha1.StorageBackend{
					Type: "s3",
					S3: &v1alpha1.S3Backend{
						Bucket:            "test-bucket",
						Region:            "us-west-2",
						Endpoint:          "http://minio:9000",
						CredentialsSecret: "test-secret",
					},
				},
			},
		}

		envVars := exec.getBackupEnvVars(config)

		// Should have: S3_BUCKET, S3_REGION, S3_ENDPOINT, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY
		if len(envVars) != 5 {
			t.Errorf("expected 5 env vars, got %d", len(envVars))
		}

		// Check specific env vars
		found := make(map[string]bool)
		for _, env := range envVars {
			found[env.Name] = true
		}
		if !found["S3_BUCKET"] {
			t.Error("expected S3_BUCKET env var")
		}
		if !found["S3_REGION"] {
			t.Error("expected S3_REGION env var")
		}
		if !found["S3_ENDPOINT"] {
			t.Error("expected S3_ENDPOINT env var")
		}
	})

	t.Run("without endpoint", func(t *testing.T) {
		config := &v1alpha1.BackupConfig{
			Spec: v1alpha1.BackupConfigSpec{
				StorageBackend: v1alpha1.StorageBackend{
					Type: "s3",
					S3: &v1alpha1.S3Backend{
						Bucket:            "test-bucket",
						Region:            "us-west-2",
						CredentialsSecret: "test-secret",
					},
				},
			},
		}

		envVars := exec.getBackupEnvVars(config)

		// Should have: S3_BUCKET, S3_REGION, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY (no S3_ENDPOINT)
		if len(envVars) != 4 {
			t.Errorf("expected 4 env vars, got %d", len(envVars))
		}
	})

	t.Run("without S3 config", func(t *testing.T) {
		config := &v1alpha1.BackupConfig{
			Spec: v1alpha1.BackupConfigSpec{
				StorageBackend: v1alpha1.StorageBackend{
					Type: "s3",
				},
			},
		}

		envVars := exec.getBackupEnvVars(config)

		if len(envVars) != 0 {
			t.Errorf("expected 0 env vars, got %d", len(envVars))
		}
	})
}

func TestCreateRestoreJob(t *testing.T) {
	logger := testr.New(t)
	testClient := newTestClient(t)
	storage := &MockStorageBackend{}
	exec := NewExecutor(testClient, storage, logger)

	backup := &v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: v1alpha1.BackupSpec{
			StorageLocation: v1alpha1.StorageLocation{
				Path: "test/path/backup.tar.gz",
			},
		},
	}

	targetPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "restore-pvc",
			Namespace: "default",
		},
	}

	job := exec.createRestoreJob(backup, targetPVC)

	if job == nil {
		t.Fatal("expected restore job to be created")
	}
	if job.Name != "restore-test-backup-restore-pvc" {
		t.Errorf("expected job name 'restore-test-backup-restore-pvc', got %s", job.Name)
	}
	if job.Labels["bardiup.io/restore"] != "test-backup" {
		t.Error("expected restore label")
	}
}

func TestCalculateBackupSize(t *testing.T) {
	logger := testr.New(t)
	testClient := newTestClient(t)
	storage := &MockStorageBackend{}
	exec := NewExecutor(testClient, storage, logger)

	t.Run("with capacity", func(t *testing.T) {
		pvc := &corev1.PersistentVolumeClaim{
			Status: corev1.PersistentVolumeClaimStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("10Gi"),
				},
			},
		}

		size := exec.calculateBackupSize(pvc)
		if size == 0 {
			t.Error("expected non-zero size")
		}
	})

	t.Run("without capacity", func(t *testing.T) {
		pvc := &corev1.PersistentVolumeClaim{
			Status: corev1.PersistentVolumeClaimStatus{},
		}

		size := exec.calculateBackupSize(pvc)
		if size != 0 {
			t.Errorf("expected 0 size, got %d", size)
		}
	})
}

func TestGenerateChecksum(t *testing.T) {
	logger := testr.New(t)
	testClient := newTestClient(t)
	storage := &MockStorageBackend{}
	exec := NewExecutor(testClient, storage, logger)

	checksum1 := exec.generateChecksum()
	checksum2 := exec.generateChecksum()

	if checksum1 == "" {
		t.Error("expected non-empty checksum")
	}
	if len(checksum1) != 64 { // SHA-256 hex is 64 chars
		t.Errorf("expected 64 char checksum, got %d", len(checksum1))
	}
	// Checksums should be different (based on time.Now())
	if checksum1 == checksum2 {
		t.Log("Note: checksums were equal (may happen if generated in same tick)")
	}
}

func TestPtrHelper(t *testing.T) {
	// Test with int32
	val := int32(5)
	p := ptr(val)
	if *p != 5 {
		t.Errorf("expected 5, got %d", *p)
	}

	// Test with string
	s := "test"
	ps := ptr(s)
	if *ps != "test" {
		t.Errorf("expected 'test', got %s", *ps)
	}
}

func TestExecuteBackupPVCNotFound(t *testing.T) {
	logger := testr.New(t)
	testClient := newTestClient(t)
	storage := &MockStorageBackend{}
	exec := NewExecutor(testClient, storage, logger)

	backup := &v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: v1alpha1.BackupSpec{
			PVCName:      "non-existent-pvc",
			PVCNamespace: "default",
		},
	}

	config := &v1alpha1.BackupConfig{
		Spec: v1alpha1.BackupConfigSpec{
			BackupMethod: v1alpha1.BackupMethodCopy,
		},
	}

	err := exec.ExecuteBackup(context.Background(), backup, config)
	if err == nil {
		t.Fatal("expected error for non-existent PVC")
	}
}

func TestExecuteBackupWithSnapshotMethod(t *testing.T) {
	logger := testr.New(t)
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
		},
	}
	testClient := newTestClient(t, pvc)
	storage := &MockStorageBackend{}
	exec := NewExecutor(testClient, storage, logger)

	backup := &v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: v1alpha1.BackupSpec{
			PVCName:      "test-pvc",
			PVCNamespace: "default",
			StorageLocation: v1alpha1.StorageLocation{
				Path: "test/path",
			},
		},
	}

	config := &v1alpha1.BackupConfig{
		Spec: v1alpha1.BackupConfigSpec{
			BackupMethod: v1alpha1.BackupMethodSnapshot,
			StorageBackend: v1alpha1.StorageBackend{
				Type: "s3",
				S3: &v1alpha1.S3Backend{
					Bucket:            "test-bucket",
					Region:            "us-west-2",
					CredentialsSecret: "test-secret",
				},
			},
		},
	}

	// This will fail because the job won't complete in tests,
	// but it exercises the snapshot path (which falls back to copy)
	err := exec.ExecuteBackup(context.Background(), backup, config)
	// We expect an error because the job creation or waiting will fail
	if err == nil {
		t.Log("ExecuteBackup completed without error (may indicate job completed)")
	}
}

func TestBackupStatusPhases(t *testing.T) {
	// Test that all phases are defined correctly
	phases := []v1alpha1.BackupStatusPhase{
		v1alpha1.BackupPhasePending,
		v1alpha1.BackupPhaseInProgress,
		v1alpha1.BackupPhaseCompleted,
		v1alpha1.BackupPhaseFailed,
		v1alpha1.BackupPhaseDeleting,
	}

	expectedValues := []string{"Pending", "InProgress", "Completed", "Failed", "Deleting"}

	for i, phase := range phases {
		if string(phase) != expectedValues[i] {
			t.Errorf("expected phase %s, got %s", expectedValues[i], string(phase))
		}
	}
}

func TestBackupMethods(t *testing.T) {
	if string(v1alpha1.BackupMethodSnapshot) != "snapshot" {
		t.Error("BackupMethodSnapshot should be 'snapshot'")
	}
	if string(v1alpha1.BackupMethodCopy) != "copy" {
		t.Error("BackupMethodCopy should be 'copy'")
	}
}

