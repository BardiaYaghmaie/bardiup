package retention

import (
	"fmt"
	"testing"
	"time"

	"github.com/bardiup/bardiup/api/v1alpha1"
	"github.com/go-logr/logr/testr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestCustomHourlyRetention tests the exact scenario:
// - Backups every 10 minutes
// - Keep 10 newest
// - Keep 1 from 1 hour ago
// - Keep 1 from 3 hours ago
func TestCustomHourlyRetention(t *testing.T) {
	log := testr.New(t)
	mgr := NewManager(nil, log)

	now := time.Date(2025, 11, 17, 13, 0, 0, 0, time.UTC)
	mgr.now = func() time.Time { return now }

	policy := v1alpha1.RetentionPolicy{
		Daily: &v1alpha1.RetentionRule{
			Keep:              10,
			SelectionStrategy: "newest",
		},
		Custom: []v1alpha1.CustomRetention{
			{
				PeriodHours:       1,
				Keep:              1,
				SelectionStrategy: "distributed",
			},
			{
				PeriodHours:       3,
				Keep:              1,
				SelectionStrategy: "distributed",
			},
		},
	}

	// Create backups:
	// - 15 recent backups (every 10 minutes, so 15 backups = 2.5 hours of backups)
	// - 1 backup from 1 hour ago
	// - 1 backup from 2 hours ago
	// - 1 backup from 3 hours ago

	backups := []v1alpha1.Backup{}

	// Create 15 recent backups (last 50 minutes, every ~3 minutes)
	// These should be the 10 newest, so they must all be newer than the 1-hour-ago backup
	for i := 0; i < 15; i++ {
		ageMinutes := 50 - (i * 3) // From 50 minutes ago to now (every 3 min, oldest is 50 min ago)
		backups = append(backups, v1alpha1.Backup{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "recent-backup-" + fmt.Sprintf("%d", i),
				CreationTimestamp: metav1.Time{Time: now.Add(-time.Duration(ageMinutes) * time.Minute)},
			},
			Spec: v1alpha1.BackupSpec{
				RetentionPeriod: "daily",
			},
		})
	}

	// Add backup from 1 hour ago (should NOT be in the 10 newest)
	backups = append(backups, v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "1hour-ago",
			CreationTimestamp: metav1.Time{Time: now.Add(-1 * time.Hour)},
		},
		Spec: v1alpha1.BackupSpec{
			RetentionPeriod: "daily",
		},
	})

	// Add backup from 2 hours ago (should be deleted - not in any retention policy)
	backups = append(backups, v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "2hours-ago",
			CreationTimestamp: metav1.Time{Time: now.Add(-2 * time.Hour)},
		},
		Spec: v1alpha1.BackupSpec{
			RetentionPeriod: "daily",
		},
	})

	// Add backup from 3 hours ago (should be kept)
	backups = append(backups, v1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "3hours-ago",
			CreationTimestamp: metav1.Time{Time: now.Add(-3 * time.Hour)},
		},
		Spec: v1alpha1.BackupSpec{
			RetentionPeriod: "daily",
		},
	})

	// Apply retention policy
	toDelete, err := mgr.ApplyRetentionPolicy(nil, &v1alpha1.BackupConfig{
		Spec: v1alpha1.BackupConfigSpec{
			RetentionPolicy: policy,
		},
	}, backups)
	if err != nil {
		t.Fatalf("Failed to apply retention policy: %v", err)
	}

	// Create a map of backups to keep (for easier lookup)
	toKeepMap := make(map[string]bool)
	for _, b := range backups {
		toKeepMap[b.Name] = true
	}
	for _, b := range toDelete {
		delete(toKeepMap, b.Name)
	}

	// Verify results
	toKeepCount := len(toKeepMap)
	expectedCount := 12 // 10 newest + 1 from 1h + 1 from 3h

	if toKeepCount != expectedCount {
		t.Errorf("Expected %d backups to keep, got %d", expectedCount, toKeepCount)
		t.Logf("Backups to keep: %v", toKeepMap)
		t.Logf("Backups to delete: %v", func() []string {
			names := make([]string, len(toDelete))
			for i, b := range toDelete {
				names[i] = b.Name
			}
			return names
		}())
	}

	// Verify specific backups
	shouldKeep := []string{
		"1hour-ago",     // Should be kept (1 hour custom retention)
		"3hours-ago",    // Should be kept (3 hours custom retention)
	}

	shouldDelete := []string{
		"2hours-ago", // Should be deleted (not in any retention policy)
	}

	for _, name := range shouldKeep {
		if !toKeepMap[name] {
			t.Errorf("Backup %s should be kept but is marked for deletion", name)
		}
	}

	for _, name := range shouldDelete {
		found := false
		for _, b := range toDelete {
			if b.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Backup %s should be deleted but is not in deletion list", name)
		}
	}

	// Verify we have 10 newest backups (out of the 15 recent ones)
	recentKept := 0
	for i := 0; i < 15; i++ {
		name := "recent-backup-" + fmt.Sprintf("%d", i)
		if toKeepMap[name] {
			recentKept++
		}
	}

	if recentKept != 10 {
		t.Errorf("Expected 10 newest backups to be kept, got %d", recentKept)
	}

	t.Logf("✅ Test passed: Keeping %d backups (10 newest + 1 from 1h + 1 from 3h)", toKeepCount)
	t.Logf("✅ Deleted %d old backups correctly", len(toDelete))
}

