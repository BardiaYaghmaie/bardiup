package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BackupConfigSpec defines which PVCs to back up, when, where to, and how long to keep the results.
type BackupConfigSpec struct {
	// Schedule in standard 5-field cron format, or a descriptor such as @daily or @every 6h.
	// +kubebuilder:validation:MinLength=1
	Schedule string `json:"schedule"`

	// TimeZone (IANA name, e.g. Europe/Berlin) used for the schedule and for calendar-based retention.
	// Defaults to UTC.
	// +optional
	TimeZone string `json:"timeZone,omitempty"`

	// PVCSelector selects PVCs in the BackupConfig's namespace. An empty selector matches every PVC.
	// +optional
	PVCSelector PVCSelector `json:"pvcSelector,omitempty"`

	// Storage is the object store receiving backup archives.
	Storage StorageSpec `json:"storage"`

	// Retention decides which completed backups are kept. Without any rule every backup is kept.
	// +optional
	Retention RetentionPolicy `json:"retention,omitempty"`

	// Method used to read volume data.
	// +kubebuilder:default=copy
	// +optional
	Method BackupMethod `json:"method,omitempty"`

	// Snapshot options for method snapshot.
	// +optional
	Snapshot *SnapshotOptions `json:"snapshot,omitempty"`

	// Excludes are glob patterns (path.Match syntax) of paths, relative to the volume root, to skip.
	// A pattern matching a directory skips its whole subtree. Example: ["lost+found", "cache/*", "*.tmp"].
	// +optional
	Excludes []string `json:"excludes,omitempty"`

	// Job customises the data mover Jobs.
	// +optional
	Job JobOptions `json:"job,omitempty"`

	// Suspend stops new scheduled backups. Retention keeps running.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// FailedBackupsHistoryLimit is how many failed backups per PVC are kept for inspection.
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=0
	// +optional
	FailedBackupsHistoryLimit *int32 `json:"failedBackupsHistoryLimit,omitempty"`
}

// PVCSelector selects PVCs by label and/or name.
type PVCSelector struct {
	// MatchLabels selects PVCs having all these labels.
	// +optional
	MatchLabels map[string]string `json:"matchLabels,omitempty"`

	// MatchExpressions are label selector requirements.
	// +optional
	MatchExpressions []metav1.LabelSelectorRequirement `json:"matchExpressions,omitempty"`

	// Names restricts the selection to these PVC names.
	// +optional
	Names []string `json:"names,omitempty"`
}

// RetentionPolicy is a Grandfather-Father-Son policy evaluated over completed backups.
//
// Each rule keeps the newest backup in each of the last N calendar periods that contain a backup
// (hours, days, ISO weeks, months, years in spec.timeZone). A backup is kept when any rule keeps it.
// Semantics match restic/borg "forget --keep-*".
type RetentionPolicy struct {
	// KeepLast always keeps the N most recent backups.
	// +kubebuilder:validation:Minimum=0
	// +optional
	KeepLast *int32 `json:"keepLast,omitempty"`

	// Hourly keeps the newest backup of each of the last N hours.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Hourly *int32 `json:"hourly,omitempty"`

	// Daily keeps the newest backup of each of the last N days.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Daily *int32 `json:"daily,omitempty"`

	// Weekly keeps the newest backup of each of the last N ISO weeks.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Weekly *int32 `json:"weekly,omitempty"`

	// Monthly keeps the newest backup of each of the last N months.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Monthly *int32 `json:"monthly,omitempty"`

	// Yearly keeps the newest backup of each of the last N years.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Yearly *int32 `json:"yearly,omitempty"`

	// Custom rules use fixed-length periods, e.g. every 90d (quarterly) or every 6h.
	// +optional
	// +listType=map
	// +listMapKey=every
	Custom []CustomRetention `json:"custom,omitempty"`
}

// CustomRetention keeps the newest backup in each of the last Keep periods of length Every.
type CustomRetention struct {
	// Every is the period length: an integer followed by h (hours), d (days) or w (weeks). Example: 90d.
	// +kubebuilder:validation:Pattern=`^[1-9][0-9]*[hdw]$`
	Every string `json:"every"`

	// Keep is the number of periods to keep a backup for.
	// +kubebuilder:validation:Minimum=1
	Keep int32 `json:"keep"`
}

// BackupConfigStatus is the observed state of a BackupConfig.
type BackupConfigStatus struct {
	// ObservedGeneration is the last spec generation processed.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastScheduleTime is when backups were last started by this config.
	// +optional
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`

	// NextScheduleTime is when the next scheduled run is due.
	// +optional
	NextScheduleTime *metav1.Time `json:"nextScheduleTime,omitempty"`

	// LastSuccessfulTime is the completion time of the newest successful backup.
	// +optional
	LastSuccessfulTime *metav1.Time `json:"lastSuccessfulTime,omitempty"`

	// LastBackupNowTrigger is the last processed value of the bardiup.io/backup-now annotation.
	// +optional
	LastBackupNowTrigger string `json:"lastBackupNowTrigger,omitempty"`

	// SelectedPVCs is the number of PVCs matched by the selector at the last reconcile.
	// +optional
	SelectedPVCs int32 `json:"selectedPVCs"`

	// CompletedBackups is the number of retained completed backups.
	// +optional
	CompletedBackups int32 `json:"completedBackups"`

	// FailedBackups is the number of retained failed backups.
	// +optional
	FailedBackups int32 `json:"failedBackups"`

	// ActiveBackups is the number of backups pending or running.
	// +optional
	ActiveBackups int32 `json:"activeBackups"`

	// Conditions: Ready (spec valid and storage reachable) and LastBackupSucceeded.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=bcfg,categories=bardiup
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 63",message="name must be at most 63 characters (it is used as a label value)"
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.schedule`
// +kubebuilder:printcolumn:name="Suspend",type=boolean,JSONPath=`.spec.suspend`
// +kubebuilder:printcolumn:name="PVCs",type=integer,JSONPath=`.status.selectedPVCs`
// +kubebuilder:printcolumn:name="Backups",type=integer,JSONPath=`.status.completedBackups`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Last Success",type=date,JSONPath=`.status.lastSuccessfulTime`
// +kubebuilder:printcolumn:name="Next",type=string,JSONPath=`.status.nextScheduleTime`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BackupConfig schedules backups of PVCs in its namespace and enforces a retention policy.
type BackupConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupConfigSpec   `json:"spec,omitempty"`
	Status BackupConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackupConfigList contains a list of BackupConfig.
type BackupConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BackupConfig `json:"items"`
}
