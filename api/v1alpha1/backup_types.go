package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeletionPolicy controls what happens to stored data when a Backup object is deleted.
// +kubebuilder:validation:Enum=Delete;Retain
type DeletionPolicy string

const (
	DeletionPolicyDelete DeletionPolicy = "Delete"
	DeletionPolicyRetain DeletionPolicy = "Retain"
)

// BackupSpec is a single backup of one PVC. BackupConfigs create these; they can also be created by hand.
type BackupSpec struct {
	// PVCName is the PVC, in the Backup's namespace, to back up.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="pvcName is immutable"
	PVCName string `json:"pvcName"`

	// Storage is the object store receiving the archive.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="storage is immutable"
	Storage StorageSpec `json:"storage"`

	// Method used to read volume data.
	// +kubebuilder:default=copy
	// +optional
	Method BackupMethod `json:"method,omitempty"`

	// Snapshot options for method snapshot.
	// +optional
	Snapshot *SnapshotOptions `json:"snapshot,omitempty"`

	// Excludes are glob patterns of paths to skip.
	// +optional
	Excludes []string `json:"excludes,omitempty"`

	// Job customises the data mover Job.
	// +optional
	Job JobOptions `json:"job,omitempty"`

	// DeletionPolicy: Delete removes the archive from object storage when this Backup is deleted; Retain keeps it.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// BackupPhase is the lifecycle phase of a Backup.
type BackupPhase string

const (
	BackupPhasePending      BackupPhase = "Pending"
	BackupPhaseSnapshotting BackupPhase = "Snapshotting"
	BackupPhaseRunning      BackupPhase = "Running"
	BackupPhaseCompleted    BackupPhase = "Completed"
	BackupPhaseFailed       BackupPhase = "Failed"
)

// BackupStatus is the observed state of a Backup.
type BackupStatus struct {
	// +optional
	Phase BackupPhase `json:"phase,omitempty"`

	// +optional
	Message string `json:"message,omitempty"`

	// ObjectKey of the archive inside the bucket. A JSON manifest is stored next to it at <objectKey>.json.
	// +optional
	ObjectKey string `json:"objectKey,omitempty"`

	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// SizeBytes is the compressed archive size.
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// Size is SizeBytes in human readable form.
	// +optional
	Size string `json:"size,omitempty"`

	// SHA256 of the archive.
	// +optional
	SHA256 string `json:"sha256,omitempty"`

	// Files is the number of entries in the archive.
	// +optional
	Files int64 `json:"files,omitempty"`

	// Source describes the PVC that was backed up, used as defaults when restoring into a new PVC.
	// +optional
	Source *SourceVolume `json:"source,omitempty"`

	// RetainedBy lists the retention rules currently keeping this backup, e.g. [daily, monthly].
	// Empty for backups that are not subject to a retention policy.
	// +optional
	RetainedBy []string `json:"retainedBy,omitempty"`

	// JobName of the data mover Job.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// SnapshotName of the VolumeSnapshot (method snapshot).
	// +optional
	SnapshotName string `json:"snapshotName,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=bk,categories=bardiup
// +kubebuilder:printcolumn:name="PVC",type=string,JSONPath=`.spec.pvcName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Size",type=string,JSONPath=`.status.size`
// +kubebuilder:printcolumn:name="Retained By",type=string,JSONPath=`.status.retainedBy`
// +kubebuilder:printcolumn:name="Completed",type=date,JSONPath=`.status.completionTime`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Backup is one archive of one PVC in object storage.
type Backup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupSpec   `json:"spec,omitempty"`
	Status BackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackupList contains a list of Backup.
type BackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Backup `json:"items"`
}
