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

// Additional tests for 100% coverage

func TestSelectBackupsForOldestStrategy(t *testing.T) {
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
		Weekly: &v1alpha1.RetentionRule{Keep: 3, SelectionStrategy: "oldest"},
	}

	keep := mgr.selectBackupsToKeep(policy, infos, now)
	// With oldest strategy over 10 days of backups split into weekly periods,
	// we keep up to 3 oldest backups
	if len(keep) == 0 {
		t.Error("expected some backups to be kept")
	}
}

func TestSelectBackupsForNewestStrategy(t *testing.T) {
	log := testr.New(t)
	mgr := NewManager(nil, log)
	now := time.Now()
	var infos []BackupInfo
	for i := 0; i < 5; i++ {
		infos = append(infos, BackupInfo{
			Backup: &v1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("backup-%d", i)},
			},
			Timestamp: now.Add(-time.Duration(i) * 24 * time.Hour),
			Age:       time.Duration(i) * 24 * time.Hour,
		})
	}

	policy := v1alpha1.RetentionPolicy{
		Daily: &v1alpha1.RetentionRule{Keep: 3, SelectionStrategy: "newest"},
	}

	keep := mgr.selectBackupsToKeep(policy, infos, now)
	if len(keep) != 3 {
		t.Errorf("expected 3 backups, got %d", len(keep))
	}
}

func TestSelectBackupsForAllPeriods(t *testing.T) {
	log := testr.New(t)
	mgr := NewManager(nil, log)
	now := time.Now()

	// Create backups spanning various time periods
	var infos []BackupInfo
	ages := []time.Duration{
		1 * 24 * time.Hour,   // 1 day
		3 * 24 * time.Hour,   // 3 days
		8 * 24 * time.Hour,   // ~1 week
		35 * 24 * time.Hour,  // ~1 month
		100 * 24 * time.Hour, // ~3 months
		400 * 24 * time.Hour, // ~1 year
	}

	for i, age := range ages {
		infos = append(infos, BackupInfo{
			Backup: &v1alpha1.Backup{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("backup-%d", i)},
			},
			Timestamp: now.Add(-age),
			Age:       age,
		})
	}

	policy := v1alpha1.RetentionPolicy{
		Daily:   &v1alpha1.RetentionRule{Keep: 2},
		Weekly:  &v1alpha1.RetentionRule{Keep: 1},
		Monthly: &v1alpha1.RetentionRule{Keep: 1},
		Yearly:  &v1alpha1.RetentionRule{Keep: 1},
	}

	keep := mgr.selectBackupsToKeep(policy, infos, now)
	if len(keep) == 0 {
		t.Error("expected some backups to be kept")
	}
}

func TestDetermineRetentionPeriodAllCases(t *testing.T) {
	log := testr.New(t)
	mgr := NewManager(nil, log)
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("daily priority when present", func(t *testing.T) {
		policy := v1alpha1.RetentionPolicy{
			Daily:  &v1alpha1.RetentionRule{Keep: 7},
			Weekly: &v1alpha1.RetentionRule{Keep: 4},
		}
		period := mgr.DetermineRetentionPeriod(policy, nil, now)
		if period != "daily" {
			t.Errorf("expected daily, got %s", period)
		}
	})

	t.Run("yearly when needed", func(t *testing.T) {
		policy := v1alpha1.RetentionPolicy{
			Yearly: &v1alpha1.RetentionRule{Keep: 2},
		}
		// No existing backups, should return yearly
		period := mgr.DetermineRetentionPeriod(policy, nil, now)
		if period != "yearly" {
			t.Errorf("expected yearly, got %s", period)
		}
	})

	t.Run("custom period when needed", func(t *testing.T) {
		policy := v1alpha1.RetentionPolicy{
			Custom: []v1alpha1.CustomRetention{
				{PeriodDays: 90, Keep: 4},
			},
		}
		period := mgr.DetermineRetentionPeriod(policy, nil, now)
		if period != "custom-d90" {
			t.Errorf("expected custom-d90, got %s", period)
		}
	})

	t.Run("custom hourly period", func(t *testing.T) {
		policy := v1alpha1.RetentionPolicy{
			Custom: []v1alpha1.CustomRetention{
				{PeriodHours: 6, Keep: 4},
			},
		}
		period := mgr.DetermineRetentionPeriod(policy, nil, now)
		if period != "custom-h6" {
			t.Errorf("expected custom-h6, got %s", period)
		}
	})

	t.Run("falls back to daily when all periods have recent backups", func(t *testing.T) {
		policy := v1alpha1.RetentionPolicy{
			Weekly: &v1alpha1.RetentionRule{Keep: 4},
		}
		backups := []v1alpha1.Backup{
			newBackupWithPeriod("weekly-backup", "weekly", now.Add(-1*time.Hour)),
		}
		period := mgr.DetermineRetentionPeriod(policy, backups, now)
		if period != "daily" {
			t.Errorf("expected daily (fallback), got %s", period)
		}
	})
}

func TestCalculateExpirationCustomHours(t *testing.T) {
	log := testr.New(t)
	mgr := NewManager(nil, log)
	creation := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	policy := v1alpha1.RetentionPolicy{
		Custom: []v1alpha1.CustomRetention{
			{PeriodHours: 6, Keep: 4},
		},
	}

	exp := mgr.CalculateExpiration(policy, "custom-h6", creation)
	expected := creation.Add(6 * time.Hour * 4)
	if !exp.Time.Equal(expected) {
		t.Errorf("expected %v, got %v", expected, exp.Time)
	}
}

func TestApplyRetentionPolicyEmptyBackups(t *testing.T) {
	log := testr.New(t)
	mgr := NewManager(nil, log)

	toDelete, err := mgr.ApplyRetentionPolicy(context.Background(), &v1alpha1.BackupConfig{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if toDelete != nil {
		t.Errorf("expected nil, got %v", toDelete)
	}
}

func TestSelectBackupsForPeriodEmptyInput(t *testing.T) {
	log := testr.New(t)
	mgr := NewManager(nil, log)
	now := time.Now()

	result := mgr.selectBackupsForPeriod(nil, now, 24*time.Hour, 3, "newest")
	if result != nil {
		t.Errorf("expected nil for empty input, got %v", result)
	}

	result = mgr.selectBackupsForPeriod(nil, now, 24*time.Hour, 0, "newest")
	if result != nil {
		t.Errorf("expected nil for keep=0, got %v", result)
	}
}

func TestContainsHelper(t *testing.T) {
	backup1 := BackupInfo{
		Backup: &v1alpha1.Backup{
			ObjectMeta: metav1.ObjectMeta{Name: "backup-1"},
		},
	}
	backup2 := BackupInfo{
		Backup: &v1alpha1.Backup{
			ObjectMeta: metav1.ObjectMeta{Name: "backup-2"},
		},
	}

	list := []BackupInfo{backup1}

	if !contains(list, backup1) {
		t.Error("expected backup1 to be found")
	}
	if contains(list, backup2) {
		t.Error("expected backup2 to not be found")
	}
}

func TestFindLastBackupForPeriod(t *testing.T) {
	log := testr.New(t)
	mgr := NewManager(nil, log)
	now := time.Now()

	backups := []v1alpha1.Backup{
		newBackupWithPeriod("backup-1", "daily", now.Add(-2*time.Hour)),
		newBackupWithPeriod("backup-2", "daily", now.Add(-1*time.Hour)),
		newBackupWithPeriod("backup-3", "weekly", now.Add(-3*time.Hour)),
	}

	t.Run("finds newest backup for period", func(t *testing.T) {
		last := mgr.findLastBackupForPeriod(backups, "daily")
		if last == nil || last.Name != "backup-2" {
			t.Errorf("expected backup-2, got %v", last)
		}
	})

	t.Run("returns nil for missing period", func(t *testing.T) {
		last := mgr.findLastBackupForPeriod(backups, "monthly")
		if last != nil {
			t.Errorf("expected nil, got %v", last)
		}
	})
}

func TestNewManager(t *testing.T) {
	log := testr.New(t)
	mgr := NewManager(nil, log)

	if mgr == nil {
		t.Fatal("expected manager to be created")
	}
	if mgr.now == nil {
		t.Error("expected now function to be set")
	}
}
