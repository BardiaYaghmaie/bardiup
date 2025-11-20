package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BackupConfigSpec defines the desired state of BackupConfig
type BackupConfigSpec struct {
	// PVCSelector defines which PVCs to backup
	PVCSelector PVCSelector `json:"pvcSelector"`

	// Schedule defines when to run backups (cron format)
	Schedule string `json:"schedule"`

	// StorageBackend defines where to store backups
	StorageBackend StorageBackend `json:"storageBackend"`

	// RetentionPolicy defines the retention rules for backups
	RetentionPolicy RetentionPolicy `json:"retentionPolicy"`

	// Paused indicates if backup schedule is paused
	Paused bool `json:"paused,omitempty"`

	// BackupMethod defines how to backup (snapshot vs copy)
	BackupMethod BackupMethod `json:"backupMethod,omitempty"`

	// SnapshotClassName is the name of the VolumeSnapshotClass to use for snapshot backups
	// +optional
	SnapshotClassName string `json:"snapshotClassName,omitempty"`
}

// PVCSelector defines how to select PVCs for backup
type PVCSelector struct {
	// Namespace to select PVCs from
	Namespace string `json:"namespace"`

	// MatchLabels is a map of labels to match PVCs
	MatchLabels map[string]string `json:"matchLabels,omitempty"`

	// MatchNames is a list of PVC names to match
	MatchNames []string `json:"matchNames,omitempty"`
}

// StorageBackend defines the storage backend configuration
type StorageBackend struct {
	// Type of storage backend (s3, azure, gcs)
	Type string `json:"type"`

	// S3 configuration (if type is s3)
	S3 *S3Backend `json:"s3,omitempty"`

	// Prefix for backup paths in object storage
	Prefix string `json:"prefix,omitempty"`
}

// S3Backend defines S3 storage configuration
type S3Backend struct {
	// Bucket name
	Bucket string `json:"bucket"`

	// Region
	Region string `json:"region"`

	// Endpoint for S3-compatible storage
	Endpoint string `json:"endpoint,omitempty"`

	// CredentialsSecret references a secret with AWS credentials
	CredentialsSecret string `json:"credentialsSecret"`

	// ForcePathStyle for S3-compatible storage
	ForcePathStyle bool `json:"forcePathStyle,omitempty"`
}

// RetentionPolicy defines dynamic retention rules
type RetentionPolicy struct {
	// Daily defines daily backup retention
	Daily *RetentionRule `json:"daily,omitempty"`

	// Weekly defines weekly backup retention
	Weekly *RetentionRule `json:"weekly,omitempty"`

	// Monthly defines monthly backup retention
	Monthly *RetentionRule `json:"monthly,omitempty"`

	// Yearly defines yearly backup retention
	Yearly *RetentionRule `json:"yearly,omitempty"`

	// Custom defines custom retention rules
	Custom []CustomRetention `json:"custom,omitempty"`
}

// RetentionRule defines how many backups to keep for a period
type RetentionRule struct {
	// Keep defines how many backups to keep
	Keep int `json:"keep"`

	// SelectionStrategy defines how to select which backups to keep (newest, oldest, distributed)
	SelectionStrategy string `json:"selectionStrategy,omitempty"`
}

// CustomRetention allows defining custom retention periods
type CustomRetention struct {
	// Period in days (e.g., 90 for quarterly)
	// If PeriodHours is set, this is ignored
	PeriodDays int `json:"periodDays,omitempty"`

	// Period in hours (e.g., 1 for 1 hour, 3 for 3 hours)
	// Takes precedence over PeriodDays if both are set
	PeriodHours int `json:"periodHours,omitempty"`

	// Keep defines how many backups to keep for this period
	Keep int `json:"keep"`

	// SelectionStrategy defines how to select which backups to keep
	SelectionStrategy string `json:"selectionStrategy,omitempty"`
}

// BackupMethod defines how to perform the backup
type BackupMethod string

const (
	// BackupMethodSnapshot uses volume snapshots
	BackupMethodSnapshot BackupMethod = "snapshot"
	// BackupMethodCopy copies data directly
	BackupMethodCopy BackupMethod = "copy"
)

// BackupConfigStatus defines the observed state of BackupConfig
type BackupConfigStatus struct {
	// LastBackupTime is the last time a backup was taken
	LastBackupTime *metav1.Time `json:"lastBackupTime,omitempty"`

	// LastSuccessfulBackupTime is the last time a backup succeeded
	LastSuccessfulBackupTime *metav1.Time `json:"lastSuccessfulBackupTime,omitempty"`

	// NextScheduledTime is when the next backup is scheduled
	NextScheduledTime *metav1.Time `json:"nextScheduledTime,omitempty"`

	// BackupCount is the total number of backups
	BackupCount int `json:"backupCount,omitempty"`

	// ActiveBackups is the list of active backup names
	ActiveBackups []string `json:"activeBackups,omitempty"`

	// Conditions represent the latest available observations
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Phase represents the current phase of the backup config
	Phase BackupConfigPhase `json:"phase,omitempty"`
}

// BackupConfigPhase represents the phase of backup configuration
type BackupConfigPhase string

const (
	BackupConfigPhaseReady   BackupConfigPhase = "Ready"
	BackupConfigPhasePending BackupConfigPhase = "Pending"
	BackupConfigPhaseError   BackupConfigPhase = "Error"
	BackupConfigPhasePaused  BackupConfigPhase = "Paused"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.schedule`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Last Backup",type=date,JSONPath=`.status.lastBackupTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BackupConfig is the Schema for the backupconfigs API
type BackupConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupConfigSpec   `json:"spec,omitempty"`
	Status BackupConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackupConfigList contains a list of BackupConfig
type BackupConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BackupConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BackupConfig{}, &BackupConfigList{})
}
