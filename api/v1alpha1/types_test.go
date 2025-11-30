package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBackupStatusPhases(t *testing.T) {
	phases := []BackupStatusPhase{
		BackupPhasePending,
		BackupPhaseInProgress,
		BackupPhaseCompleted,
		BackupPhaseFailed,
		BackupPhaseDeleting,
	}

	expectedValues := []string{"Pending", "InProgress", "Completed", "Failed", "Deleting"}

	for i, phase := range phases {
		if string(phase) != expectedValues[i] {
			t.Errorf("expected phase %s, got %s", expectedValues[i], string(phase))
		}
	}
}

func TestBackupMethods(t *testing.T) {
	if string(BackupMethodSnapshot) != "snapshot" {
		t.Error("BackupMethodSnapshot should be 'snapshot'")
	}
	if string(BackupMethodCopy) != "copy" {
		t.Error("BackupMethodCopy should be 'copy'")
	}
}

func TestBackupConfigPhases(t *testing.T) {
	phases := []BackupConfigPhase{
		BackupConfigPhaseReady,
		BackupConfigPhasePending,
		BackupConfigPhaseError,
		BackupConfigPhasePaused,
	}

	expectedValues := []string{"Ready", "Pending", "Error", "Paused"}

	for i, phase := range phases {
		if string(phase) != expectedValues[i] {
			t.Errorf("expected phase %s, got %s", expectedValues[i], string(phase))
		}
	}
}

func TestBackupSpec(t *testing.T) {
	now := metav1.Now()
	spec := BackupSpec{
		BackupConfig:    "test-config",
		PVCName:         "test-pvc",
		PVCNamespace:    "default",
		RetentionPeriod: "daily",
		ExpiresAt:       &now,
		Labels: map[string]string{
			"key": "value",
		},
		StorageLocation: StorageLocation{
			Type:   "s3",
			Path:   "/backups/test",
			Bucket: "test-bucket",
			Size:   1024,
		},
	}

	if spec.BackupConfig != "test-config" {
		t.Errorf("expected BackupConfig 'test-config', got %s", spec.BackupConfig)
	}
	if spec.PVCName != "test-pvc" {
		t.Errorf("expected PVCName 'test-pvc', got %s", spec.PVCName)
	}
	if spec.StorageLocation.Type != "s3" {
		t.Errorf("expected StorageLocation.Type 's3', got %s", spec.StorageLocation.Type)
	}
}

func TestBackupStatus(t *testing.T) {
	now := metav1.Now()
	status := BackupStatus{
		Phase:              BackupPhaseCompleted,
		StartTime:          &now,
		CompletionTime:     &now,
		Message:            "Backup completed",
		VolumeSnapshotName: "snap-123",
		BackupMethod:       BackupMethodCopy,
		Checksum:           "abc123",
		Conditions: []metav1.Condition{
			{
				Type:   "Ready",
				Status: metav1.ConditionTrue,
			},
		},
	}

	if status.Phase != BackupPhaseCompleted {
		t.Errorf("expected phase Completed, got %s", status.Phase)
	}
	if status.Message != "Backup completed" {
		t.Errorf("expected message 'Backup completed', got %s", status.Message)
	}
	if len(status.Conditions) != 1 {
		t.Errorf("expected 1 condition, got %d", len(status.Conditions))
	}
}

func TestBackupConfigSpec(t *testing.T) {
	spec := BackupConfigSpec{
		Schedule:     "0 2 * * *",
		Paused:       false,
		BackupMethod: BackupMethodSnapshot,
		PVCSelector: PVCSelector{
			Namespace:   "default",
			MatchLabels: map[string]string{"app": "test"},
			MatchNames:  []string{"pvc1", "pvc2"},
		},
		StorageBackend: StorageBackend{
			Type:   "s3",
			Prefix: "backups",
			S3: &S3Backend{
				Bucket:            "test-bucket",
				Region:            "us-west-2",
				Endpoint:          "http://minio:9000",
				CredentialsSecret: "aws-creds",
				ForcePathStyle:    true,
			},
		},
		RetentionPolicy: RetentionPolicy{
			Daily:   &RetentionRule{Keep: 7, SelectionStrategy: "newest"},
			Weekly:  &RetentionRule{Keep: 4},
			Monthly: &RetentionRule{Keep: 12},
			Yearly:  &RetentionRule{Keep: 5},
			Custom: []CustomRetention{
				{PeriodDays: 90, Keep: 4},
				{PeriodHours: 6, Keep: 2, SelectionStrategy: "distributed"},
			},
		},
	}

	if spec.Schedule != "0 2 * * *" {
		t.Errorf("expected schedule '0 2 * * *', got %s", spec.Schedule)
	}
	if spec.PVCSelector.Namespace != "default" {
		t.Errorf("expected namespace 'default', got %s", spec.PVCSelector.Namespace)
	}
	if spec.StorageBackend.S3.Bucket != "test-bucket" {
		t.Errorf("expected bucket 'test-bucket', got %s", spec.StorageBackend.S3.Bucket)
	}
	if spec.RetentionPolicy.Daily.Keep != 7 {
		t.Errorf("expected daily keep 7, got %d", spec.RetentionPolicy.Daily.Keep)
	}
	if len(spec.RetentionPolicy.Custom) != 2 {
		t.Errorf("expected 2 custom retention rules, got %d", len(spec.RetentionPolicy.Custom))
	}
}

func TestBackupConfigStatus(t *testing.T) {
	now := metav1.Now()
	status := BackupConfigStatus{
		LastBackupTime:           &now,
		LastSuccessfulBackupTime: &now,
		NextScheduledTime:        &now,
		BackupCount:              10,
		ActiveBackups:            []string{"backup-1", "backup-2"},
		Phase:                    BackupConfigPhaseReady,
		Conditions: []metav1.Condition{
			{
				Type:   "Ready",
				Status: metav1.ConditionTrue,
			},
		},
	}

	if status.BackupCount != 10 {
		t.Errorf("expected backup count 10, got %d", status.BackupCount)
	}
	if len(status.ActiveBackups) != 2 {
		t.Errorf("expected 2 active backups, got %d", len(status.ActiveBackups))
	}
	if status.Phase != BackupConfigPhaseReady {
		t.Errorf("expected phase Ready, got %s", status.Phase)
	}
}

func TestStorageLocation(t *testing.T) {
	loc := StorageLocation{
		Type:   "s3",
		Path:   "/backups/test.tar.gz",
		Bucket: "my-bucket",
		Size:   1048576, // 1 MB
	}

	if loc.Type != "s3" {
		t.Errorf("expected type 's3', got %s", loc.Type)
	}
	if loc.Size != 1048576 {
		t.Errorf("expected size 1048576, got %d", loc.Size)
	}
}

func TestPVCSelector(t *testing.T) {
	selector := PVCSelector{
		Namespace:   "production",
		MatchLabels: map[string]string{"tier": "frontend"},
		MatchNames:  []string{"app-data", "config-pvc"},
	}

	if selector.Namespace != "production" {
		t.Errorf("expected namespace 'production', got %s", selector.Namespace)
	}
	if len(selector.MatchLabels) != 1 {
		t.Errorf("expected 1 match label, got %d", len(selector.MatchLabels))
	}
	if len(selector.MatchNames) != 2 {
		t.Errorf("expected 2 match names, got %d", len(selector.MatchNames))
	}
}

func TestRetentionRule(t *testing.T) {
	rule := RetentionRule{
		Keep:              5,
		SelectionStrategy: "distributed",
	}

	if rule.Keep != 5 {
		t.Errorf("expected keep 5, got %d", rule.Keep)
	}
	if rule.SelectionStrategy != "distributed" {
		t.Errorf("expected strategy 'distributed', got %s", rule.SelectionStrategy)
	}
}

func TestCustomRetention(t *testing.T) {
	t.Run("days-based", func(t *testing.T) {
		custom := CustomRetention{
			PeriodDays:        90,
			Keep:              4,
			SelectionStrategy: "newest",
		}

		if custom.PeriodDays != 90 {
			t.Errorf("expected period days 90, got %d", custom.PeriodDays)
		}
	})

	t.Run("hours-based", func(t *testing.T) {
		custom := CustomRetention{
			PeriodHours:       6,
			Keep:              4,
			SelectionStrategy: "distributed",
		}

		if custom.PeriodHours != 6 {
			t.Errorf("expected period hours 6, got %d", custom.PeriodHours)
		}
	})
}

func TestS3Backend(t *testing.T) {
	s3 := S3Backend{
		Bucket:            "test-bucket",
		Region:            "eu-west-1",
		Endpoint:          "https://s3.amazonaws.com",
		CredentialsSecret: "aws-secret",
		ForcePathStyle:    false,
	}

	if s3.Bucket != "test-bucket" {
		t.Errorf("expected bucket 'test-bucket', got %s", s3.Bucket)
	}
	if s3.Region != "eu-west-1" {
		t.Errorf("expected region 'eu-west-1', got %s", s3.Region)
	}
	if s3.ForcePathStyle {
		t.Error("expected ForcePathStyle to be false")
	}
}

func TestBackupObject(t *testing.T) {
	backup := Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: BackupSpec{
			BackupConfig: "test-config",
			PVCName:      "test-pvc",
		},
		Status: BackupStatus{
			Phase: BackupPhasePending,
		},
	}

	if backup.Name != "test-backup" {
		t.Errorf("expected name 'test-backup', got %s", backup.Name)
	}
	if backup.Spec.BackupConfig != "test-config" {
		t.Errorf("expected config 'test-config', got %s", backup.Spec.BackupConfig)
	}
}

func TestBackupListObject(t *testing.T) {
	list := BackupList{
		Items: []Backup{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "backup-1"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "backup-2"},
			},
		},
	}

	if len(list.Items) != 2 {
		t.Errorf("expected 2 items, got %d", len(list.Items))
	}
}

func TestBackupConfigObject(t *testing.T) {
	config := BackupConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-config",
		},
		Spec: BackupConfigSpec{
			Schedule: "0 2 * * *",
		},
		Status: BackupConfigStatus{
			Phase: BackupConfigPhaseReady,
		},
	}

	if config.Name != "test-config" {
		t.Errorf("expected name 'test-config', got %s", config.Name)
	}
}

func TestBackupConfigListObject(t *testing.T) {
	list := BackupConfigList{
		Items: []BackupConfig{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "config-1"},
			},
		},
	}

	if len(list.Items) != 1 {
		t.Errorf("expected 1 item, got %d", len(list.Items))
	}
}

func TestGroupVersion(t *testing.T) {
	if GroupVersion.Group != "bardiup.io" {
		t.Errorf("expected group 'bardiup.io', got %s", GroupVersion.Group)
	}
	if GroupVersion.Version != "v1alpha1" {
		t.Errorf("expected version 'v1alpha1', got %s", GroupVersion.Version)
	}
}

