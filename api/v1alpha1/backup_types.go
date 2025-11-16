package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BackupSpec defines the desired state of Backup
type BackupSpec struct {
	// BackupConfig references the BackupConfig that created this backup
	BackupConfig string `json:"backupConfig"`

	// PVCName is the name of the PVC being backed up
	PVCName string `json:"pvcName"`

	// PVCNamespace is the namespace of the PVC being backed up
	PVCNamespace string `json:"pvcNamespace"`

	// StorageLocation is where the backup is stored
	StorageLocation StorageLocation `json:"storageLocation"`

	// RetentionPeriod defines which retention period this backup belongs to
	RetentionPeriod string `json:"retentionPeriod,omitempty"`

	// ExpiresAt defines when this backup should be deleted
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`

	// Labels to help with retention selection
	Labels map[string]string `json:"labels,omitempty"`
}

// StorageLocation defines where a backup is stored
type StorageLocation struct {
	// Type of storage (s3, azure, gcs)
	Type string `json:"type"`

	// Path is the full path to the backup in object storage
	Path string `json:"path"`

	// Bucket name (for S3-compatible storage)
	Bucket string `json:"bucket,omitempty"`

	// Size of the backup in bytes
	Size int64 `json:"size,omitempty"`
}

// BackupStatus defines the observed state of Backup
type BackupStatus struct {
	// Phase of the backup
	Phase BackupStatusPhase `json:"phase,omitempty"`

	// StartTime when the backup started
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime when the backup completed
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Message provides details about the backup status
	Message string `json:"message,omitempty"`

	// VolumeSnapshotName if using snapshot method
	VolumeSnapshotName string `json:"volumeSnapshotName,omitempty"`

	// BackupMethod used for this backup
	BackupMethod BackupMethod `json:"backupMethod,omitempty"`

	// Checksum of the backup data
	Checksum string `json:"checksum,omitempty"`

	// Conditions represent the latest available observations
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// BackupStatusPhase represents the phase of a backup
type BackupStatusPhase string

const (
	BackupPhasePending    BackupStatusPhase = "Pending"
	BackupPhaseInProgress BackupStatusPhase = "InProgress"
	BackupPhaseCompleted  BackupStatusPhase = "Completed"
	BackupPhaseFailed     BackupStatusPhase = "Failed"
	BackupPhaseDeleting   BackupStatusPhase = "Deleting"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="PVC",type=string,JSONPath=`.spec.pvcName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Size",type=string,JSONPath=`.spec.storageLocation.size`
// +kubebuilder:printcolumn:name="Expires",type=date,JSONPath=`.spec.expiresAt`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Backup is the Schema for the backups API
type Backup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupSpec   `json:"spec,omitempty"`
	Status BackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackupList contains a list of Backup
type BackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Backup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Backup{}, &BackupList{})
}
