package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RestoreSpec restores backup data into a PVC.
// +kubebuilder:validation:XValidation:rule="has(self.backupName) != has(self.source)",message="exactly one of backupName or source must be set"
type RestoreSpec struct {
	// BackupName is a Completed Backup in the Restore's namespace.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="backupName is immutable"
	// +optional
	BackupName string `json:"backupName,omitempty"`

	// Source restores straight from an archive in object storage, without a Backup object.
	// Use it for disaster recovery into a new cluster or namespace.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="source is immutable"
	// +optional
	Source *RestoreSource `json:"source,omitempty"`

	// Target is the PVC receiving the data.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="target is immutable"
	Target RestoreTarget `json:"target"`

	// Job customises the data mover Job.
	// +optional
	Job JobOptions `json:"job,omitempty"`
}

// RestoreSource points at an archive written by Bardiup.
type RestoreSource struct {
	// Storage holding the archive. The credentials Secret must exist in the Restore's namespace.
	Storage StorageSpec `json:"storage"`

	// ObjectKey of the .tar.gz archive, e.g. prod/postgres/data-postgres-0/nightly-data-postgres-0-20261119-020000.tar.gz.
	// If <objectKey>.json exists, its checksum is verified.
	// +kubebuilder:validation:MinLength=1
	ObjectKey string `json:"objectKey"`
}

// RestoreTarget names the destination PVC. If it does not exist it is created,
// using the source PVC's storage class, access modes and capacity unless overridden here.
// When restoring from spec.source into a PVC that does not exist yet, size is required.
type RestoreTarget struct {
	// PVCName of the destination PVC in the Restore's namespace.
	// +kubebuilder:validation:MinLength=1
	PVCName string `json:"pvcName"`

	// StorageClassName for a newly created PVC.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// Size for a newly created PVC.
	// +optional
	Size *resource.Quantity `json:"size,omitempty"`

	// AccessModes for a newly created PVC.
	// +optional
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`

	// CleanTarget deletes all existing content of the target volume before extracting.
	// Without it, restored files overwrite existing ones and unrelated files are left in place.
	// +optional
	CleanTarget bool `json:"cleanTarget,omitempty"`
}

// RestorePhase is the lifecycle phase of a Restore.
type RestorePhase string

const (
	RestorePhasePending   RestorePhase = "Pending"
	RestorePhaseRunning   RestorePhase = "Running"
	RestorePhaseCompleted RestorePhase = "Completed"
	RestorePhaseFailed    RestorePhase = "Failed"
)

// RestoreStatus is the observed state of a Restore.
type RestoreStatus struct {
	// +optional
	Phase RestorePhase `json:"phase,omitempty"`

	// +optional
	Message string `json:"message,omitempty"`

	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// PVCCreated is true when the target PVC was created by this Restore.
	// +optional
	PVCCreated bool `json:"pvcCreated,omitempty"`

	// Files restored.
	// +optional
	Files int64 `json:"files,omitempty"`

	// +optional
	JobName string `json:"jobName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=rst,categories=bardiup
// +kubebuilder:printcolumn:name="Backup",type=string,JSONPath=`.spec.backupName`
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.source.objectKey`,priority=1
// +kubebuilder:printcolumn:name="Target PVC",type=string,JSONPath=`.spec.target.pvcName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Restore extracts a Backup into a new or existing PVC.
type Restore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RestoreSpec   `json:"spec,omitempty"`
	Status RestoreStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RestoreList contains a list of Restore.
type RestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Restore `json:"items"`
}
