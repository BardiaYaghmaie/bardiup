package retention

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/bardiup/bardiup/api/v1alpha1"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Manager handles backup retention policies
type Manager struct {
	client client.Client
	log    logr.Logger
}

// NewManager creates a new retention manager
func NewManager(client client.Client, log logr.Logger) *Manager {
	return &Manager{
		client: client,
		log:    log,
	}
}

// BackupInfo contains information about a backup for retention decisions
type BackupInfo struct {
	Backup    *v1alpha1.Backup
	Timestamp time.Time
	Age       time.Duration
}

// ApplyRetentionPolicy applies the retention policy to existing backups
func (m *Manager) ApplyRetentionPolicy(ctx context.Context, config *v1alpha1.BackupConfig, backups []v1alpha1.Backup) ([]v1alpha1.Backup, error) {
	if len(backups) == 0 {
		return nil, nil
	}

	// Convert backups to BackupInfo for easier processing
	backupInfos := make([]BackupInfo, 0, len(backups))
	now := time.Now()

	for i := range backups {
		backup := &backups[i]
		timestamp := backup.CreationTimestamp.Time
		backupInfos = append(backupInfos, BackupInfo{
			Backup:    backup,
			Timestamp: timestamp,
			Age:       now.Sub(timestamp),
		})
	}

	// Sort backups by timestamp (newest first)
	sort.Slice(backupInfos, func(i, j int) bool {
		return backupInfos[i].Timestamp.After(backupInfos[j].Timestamp)
	})

	// Determine which backups to keep
	toKeep := m.selectBackupsToKeep(config.Spec.RetentionPolicy, backupInfos, now)

	// Mark backups for deletion
	toDelete := []v1alpha1.Backup{}
	for _, info := range backupInfos {
		if !contains(toKeep, info) {
			toDelete = append(toDelete, *info.Backup)
			m.log.Info("Marking backup for deletion",
				"backup", info.Backup.Name,
				"age", info.Age,
				"timestamp", info.Timestamp)
		}
	}

	return toDelete, nil
}

// selectBackupsToKeep determines which backups should be kept based on retention policy
func (m *Manager) selectBackupsToKeep(policy v1alpha1.RetentionPolicy, backups []BackupInfo, now time.Time) []BackupInfo {
	toKeep := make(map[string]BackupInfo)

	// Process daily retention
	if policy.Daily != nil && policy.Daily.Keep > 0 {
		dailyBackups := m.selectBackupsForPeriod(backups, now, 24*time.Hour, policy.Daily.Keep, policy.Daily.SelectionStrategy)
		for _, b := range dailyBackups {
			toKeep[b.Backup.Name] = b
		}
	}

	// Process weekly retention
	if policy.Weekly != nil && policy.Weekly.Keep > 0 {
		weeklyBackups := m.selectBackupsForPeriod(backups, now, 7*24*time.Hour, policy.Weekly.Keep, policy.Weekly.SelectionStrategy)
		for _, b := range weeklyBackups {
			toKeep[b.Backup.Name] = b
		}
	}

	// Process monthly retention
	if policy.Monthly != nil && policy.Monthly.Keep > 0 {
		monthlyBackups := m.selectBackupsForPeriod(backups, now, 30*24*time.Hour, policy.Monthly.Keep, policy.Monthly.SelectionStrategy)
		for _, b := range monthlyBackups {
			toKeep[b.Backup.Name] = b
		}
	}

	// Process yearly retention
	if policy.Yearly != nil && policy.Yearly.Keep > 0 {
		yearlyBackups := m.selectBackupsForPeriod(backups, now, 365*24*time.Hour, policy.Yearly.Keep, policy.Yearly.SelectionStrategy)
		for _, b := range yearlyBackups {
			toKeep[b.Backup.Name] = b
		}
	}

	// Process custom retention periods
	for _, custom := range policy.Custom {
		if custom.Keep > 0 {
			period := time.Duration(custom.PeriodDays) * 24 * time.Hour
			customBackups := m.selectBackupsForPeriod(backups, now, period, custom.Keep, custom.SelectionStrategy)
			for _, b := range customBackups {
				toKeep[b.Backup.Name] = b
			}
		}
	}

	// Convert map to slice
	result := make([]BackupInfo, 0, len(toKeep))
	for _, backup := range toKeep {
		result = append(result, backup)
	}

	return result
}

// selectBackupsForPeriod selects backups to keep for a specific period
func (m *Manager) selectBackupsForPeriod(backups []BackupInfo, now time.Time, period time.Duration, keep int, strategy string) []BackupInfo {
	if len(backups) == 0 || keep <= 0 {
		return nil
	}

	// Group backups by period
	periodBackups := make(map[int][]BackupInfo)

	for _, backup := range backups {
		periodIndex := int(backup.Age / period)
		periodBackups[periodIndex] = append(periodBackups[periodIndex], backup)
	}

	// Select backups based on strategy
	selected := []BackupInfo{}

	switch strategy {
	case "distributed":
		// Try to keep backups distributed across time periods
		periods := make([]int, 0, len(periodBackups))
		for p := range periodBackups {
			periods = append(periods, p)
		}
		sort.Ints(periods)

		// Calculate step size for distribution
		step := 1
		if len(periods) > keep {
			step = len(periods) / keep
		}

		for i := 0; i < len(periods) && len(selected) < keep; i += step {
			if backupsInPeriod, exists := periodBackups[periods[i]]; exists && len(backupsInPeriod) > 0 {
				// Select the newest backup from this period
				selected = append(selected, backupsInPeriod[0])
			}
		}

	case "oldest":
		// Keep the oldest backups
		allBackups := []BackupInfo{}
		for _, periodList := range periodBackups {
			allBackups = append(allBackups, periodList...)
		}
		sort.Slice(allBackups, func(i, j int) bool {
			return allBackups[i].Timestamp.Before(allBackups[j].Timestamp)
		})
		for i := 0; i < keep && i < len(allBackups); i++ {
			selected = append(selected, allBackups[i])
		}

	default: // "newest" or unspecified
		// Keep the newest backups (default behavior)
		for i := 0; i < keep && i < len(backups); i++ {
			selected = append(selected, backups[i])
		}
	}

	return selected
}

// CalculateExpiration calculates when a backup should expire based on retention policy
func (m *Manager) CalculateExpiration(policy v1alpha1.RetentionPolicy, retentionPeriod string, creationTime time.Time) *metav1.Time {
	var expiration time.Time

	switch retentionPeriod {
	case "daily":
		if policy.Daily != nil && policy.Daily.Keep > 0 {
			expiration = creationTime.Add(time.Duration(policy.Daily.Keep) * 24 * time.Hour)
		}
	case "weekly":
		if policy.Weekly != nil && policy.Weekly.Keep > 0 {
			expiration = creationTime.Add(time.Duration(policy.Weekly.Keep) * 7 * 24 * time.Hour)
		}
	case "monthly":
		if policy.Monthly != nil && policy.Monthly.Keep > 0 {
			expiration = creationTime.Add(time.Duration(policy.Monthly.Keep) * 30 * 24 * time.Hour)
		}
	case "yearly":
		if policy.Yearly != nil && policy.Yearly.Keep > 0 {
			expiration = creationTime.Add(time.Duration(policy.Yearly.Keep) * 365 * 24 * time.Hour)
		}
	default:
		// Check custom retention periods
		for _, custom := range policy.Custom {
			if fmt.Sprintf("custom-%d", custom.PeriodDays) == retentionPeriod {
				expiration = creationTime.Add(time.Duration(custom.PeriodDays*custom.Keep) * 24 * time.Hour)
				break
			}
		}
	}

	if expiration.IsZero() {
		// Default expiration if no matching period found
		expiration = creationTime.Add(7 * 24 * time.Hour) // 7 days default
	}

	return &metav1.Time{Time: expiration}
}

// DetermineRetentionPeriod determines which retention period a new backup should belong to
func (m *Manager) DetermineRetentionPeriod(policy v1alpha1.RetentionPolicy, existingBackups []v1alpha1.Backup, now time.Time) string {
	// Count existing backups by period
	periodCounts := make(map[string]int)
	for _, backup := range existingBackups {
		if backup.Spec.RetentionPeriod != "" {
			periodCounts[backup.Spec.RetentionPeriod]++
		}
	}

	// Determine which period needs a backup
	// Priority: daily -> weekly -> monthly -> yearly -> custom
	if policy.Daily != nil && policy.Daily.Keep > 0 {
		return "daily"
	}

	// Check if we need a weekly backup
	if policy.Weekly != nil && policy.Weekly.Keep > 0 {
		lastWeeklyBackup := m.findLastBackupForPeriod(existingBackups, "weekly")
		if lastWeeklyBackup == nil || now.Sub(lastWeeklyBackup.CreationTimestamp.Time) > 7*24*time.Hour {
			return "weekly"
		}
	}

	// Check if we need a monthly backup
	if policy.Monthly != nil && policy.Monthly.Keep > 0 {
		lastMonthlyBackup := m.findLastBackupForPeriod(existingBackups, "monthly")
		if lastMonthlyBackup == nil || now.Sub(lastMonthlyBackup.CreationTimestamp.Time) > 30*24*time.Hour {
			return "monthly"
		}
	}

	// Check if we need a yearly backup
	if policy.Yearly != nil && policy.Yearly.Keep > 0 {
		lastYearlyBackup := m.findLastBackupForPeriod(existingBackups, "yearly")
		if lastYearlyBackup == nil || now.Sub(lastYearlyBackup.CreationTimestamp.Time) > 365*24*time.Hour {
			return "yearly"
		}
	}

	// Check custom periods
	for _, custom := range policy.Custom {
		periodName := fmt.Sprintf("custom-%d", custom.PeriodDays)
		lastCustomBackup := m.findLastBackupForPeriod(existingBackups, periodName)
		period := time.Duration(custom.PeriodDays) * 24 * time.Hour
		if lastCustomBackup == nil || now.Sub(lastCustomBackup.CreationTimestamp.Time) > period {
			return periodName
		}
	}

	// No period matched
	return ""
}

func (m *Manager) findLastBackupForPeriod(backups []v1alpha1.Backup, period string) *v1alpha1.Backup {
	var lastBackup *v1alpha1.Backup
	for i := range backups {
		backup := &backups[i]
		if backup.Spec.RetentionPeriod == period {
			if lastBackup == nil || backup.CreationTimestamp.Time.After(lastBackup.CreationTimestamp.Time) {
				lastBackup = backup
			}
		}
	}
	return lastBackup
}

func contains(backups []BackupInfo, backup BackupInfo) bool {
	for _, b := range backups {
		if b.Backup.Name == backup.Backup.Name {
			return true
		}
	}
	return false
}
