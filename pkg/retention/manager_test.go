package retention

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bardiup/bardiup/api/v1alpha1"
	"github.com/go-logr/logr/testr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCalculateExpiration(t *testing.T) {
	log := testr.New(t)
	mgr := NewManager(nil, log)

	creation := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := v1alpha1.RetentionPolicy{
		Daily:   &v1alpha1.RetentionRule{Keep: 7},
		Weekly:  &v1alpha1.RetentionRule{Keep: 4},
		Monthly: &v1alpha1.RetentionRule{Keep: 3},
		Yearly:  &v1alpha1.RetentionRule{Keep: 2},
		Custom: []v1alpha1.CustomRetention{
			{PeriodDays: 90, Keep: 1},
		},
	}

	tests := []struct {
		name     string
		period   string
		expected time.Time
	}{
		{"daily", "daily", creation.Add(7 * 24 * time.Hour)},
		{"weekly", "weekly", creation.Add(4 * 7 * 24 * time.Hour)},
		{"monthly", "monthly", creation.Add(3 * 30 * 24 * time.Hour)},
		{"yearly", "yearly", creation.Add(2 * 365 * 24 * time.Hour)},
		{"custom", "custom-d90", creation.Add(90 * 24 * time.Hour)},
		{"default", "unknown", creation.Add(7 * 24 * time.Hour)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp := mgr.CalculateExpiration(policy, tt.period, creation)
			if exp == nil || !exp.Time.Equal(tt.expected) {
				t.Fatalf("expected %v, got %v", tt.expected, exp)
			}
		})
	}
}

func TestDetermineRetentionPeriod(t *testing.T) {
	log := testr.New(t)
	mgr := NewManager(nil, log)

	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := v1alpha1.RetentionPolicy{
		Weekly:  &v1alpha1.RetentionRule{Keep: 4},
		Monthly: &v1alpha1.RetentionRule{Keep: 6},
		Custom: []v1alpha1.CustomRetention{
			{PeriodDays: 90, Keep: 2},
		},
	}

	backups := []v1alpha1.Backup{
		newBackupWithPeriod("daily-backup", "daily", now.Add(-2*time.Hour)),
		newBackupWithPeriod("weekly-backup", "weekly", now.Add(-8*24*time.Hour)),
	}

	period := mgr.DetermineRetentionPeriod(policy, backups, now)
	if period != "weekly" {
		t.Fatalf("expected weekly period, got %s", period)
	}

	backups = append(backups, newBackupWithPeriod("weekly-backup-new", "weekly", now.Add(-2*time.Hour)))
	period = mgr.DetermineRetentionPeriod(policy, backups, now)
	if period != "monthly" {
		t.Fatalf("expected monthly period, got %s", period)
	}
}

func TestApplyRetentionPolicy(t *testing.T) {
	log := testr.New(t)
	mgr := NewManager(nil, log)
	policy := v1alpha1.RetentionPolicy{
		Daily: &v1alpha1.RetentionRule{Keep: 2, SelectionStrategy: "newest"},
	}

	now := time.Now()
	backups := []v1alpha1.Backup{
		newBackupWithTime("oldest", now.Add(-10*24*time.Hour)),
		newBackupWithTime("older", now.Add(-9*24*time.Hour)),
		newBackupWithTime("new", now.Add(-1*24*time.Hour)),
	}

	toDelete, err := mgr.ApplyRetentionPolicy(context.Background(), &v1alpha1.BackupConfig{
		Spec: v1alpha1.BackupConfigSpec{
			RetentionPolicy: policy,
		},
	}, backups)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(toDelete) != 1 || toDelete[0].Name != "oldest" {
		t.Fatalf("expected to delete oldest backup, got %#v", toDelete)
	}
}

func TestSelectBackupsForDistributedStrategy(t *testing.T) {
	log := testr.New(t)
	mgr := NewManager(nil, log)
	now := time.Now()
	var infos []BackupInfo
	for i := 0; i < 10; i++ {
		infos = append(infos, BackupInfo{
			Backup: &v1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("backup-%d", i)},
			},
			Timestamp: now.Add(-time.Duration(i) * 24 * time.Hour),
			Age:       time.Duration(i) * 24 * time.Hour,
		})
	}

	policy := v1alpha1.RetentionPolicy{
		Weekly: &v1alpha1.RetentionRule{Keep: 3, SelectionStrategy: "distributed"},
	}

	keep := mgr.selectBackupsToKeep(policy, infos, now)
	if len(keep) != 2 {
		t.Fatalf("expected 2 backups to keep due to available periods, got %d", len(keep))
	}
}

func newBackupWithPeriod(name, period string, timestamp time.Time) v1alpha1.Backup {
	return v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: metav1.NewTime(timestamp),
		},
		Spec: v1alpha1.BackupSpec{
			RetentionPeriod: period,
		},
	}
}

func newBackupWithTime(name string, timestamp time.Time) v1alpha1.Backup {
	return v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: metav1.NewTime(timestamp),
		},
	}
}
