package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
)

// Label and annotation keys used by Bardiup.
const (
	LabelBackupConfig = "bardiup.io/backup-config"
	LabelPVC          = "bardiup.io/pvc"
	LabelComponent    = "app.kubernetes.io/component"
	LabelManagedBy    = "app.kubernetes.io/managed-by"
	ManagedByValue    = "bardiup"

	// AnnotationBackupNow triggers an immediate backup when its value changes.
	AnnotationBackupNow = "bardiup.io/backup-now"

	Finalizer = "bardiup.io/cleanup"
)

// StorageSpec describes where backup archives are stored.
type StorageSpec struct {
	// S3 configures an S3 or S3-compatible bucket (AWS, MinIO, Ceph RGW, Cloudflare R2, Backblaze B2, Wasabi, ...).
	S3 S3Storage `json:"s3"`
}

// S3Storage configures an S3-compatible bucket.
type S3Storage struct {
	// Bucket name. It must already exist.
	// +kubebuilder:validation:MinLength=3
	Bucket string `json:"bucket"`

	// Region of the bucket. Most S3-compatible stores accept any value.
	// +kubebuilder:default=us-east-1
	// +optional
	Region string `json:"region,omitempty"`

	// Endpoint URL for S3-compatible storage, e.g. https://minio.example.com:9000.
	// Leave empty for AWS S3.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Prefix prepended to every object key. Objects are stored as
	// <prefix>/<namespace>/<pvc>/<backup>.tar.gz plus a .json manifest.
	// +optional
	Prefix string `json:"prefix,omitempty"`

	// ForcePathStyle uses path-style addressing (bucket in the path). Required by most self-hosted stores like MinIO.
	// +optional
	ForcePathStyle bool `json:"forcePathStyle,omitempty"`

	// InsecureSkipTLSVerify disables TLS certificate verification. Only for testing.
	// +optional
	InsecureSkipTLSVerify bool `json:"insecureSkipTLSVerify,omitempty"`

	// CredentialsSecretRef names a Secret in the same namespace holding AWS_ACCESS_KEY_ID,
	// AWS_SECRET_ACCESS_KEY and optionally AWS_SESSION_TOKEN.
	// When omitted, the default AWS credential chain is used (IRSA, EKS Pod Identity, instance profile).
	// +optional
	CredentialsSecretRef *corev1.LocalObjectReference `json:"credentialsSecretRef,omitempty"`

	// ServerSideEncryption requests server-side encryption of stored objects.
	// +kubebuilder:validation:Enum=AES256;aws:kms
	// +optional
	ServerSideEncryption string `json:"serverSideEncryption,omitempty"`

	// KMSKeyID is the KMS key used when serverSideEncryption is aws:kms.
	// +optional
	KMSKeyID string `json:"kmsKeyID,omitempty"`
}

// BackupMethod selects how volume data is read.
// +kubebuilder:validation:Enum=copy;snapshot
type BackupMethod string

const (
	// BackupMethodCopy mounts the live PVC read-only and archives it.
	BackupMethodCopy BackupMethod = "copy"
	// BackupMethodSnapshot takes a CSI VolumeSnapshot, clones it into a temporary PVC and archives the clone.
	BackupMethodSnapshot BackupMethod = "snapshot"
)

// SnapshotOptions configures the snapshot backup method.
type SnapshotOptions struct {
	// VolumeSnapshotClassName to use. Defaults to the cluster default VolumeSnapshotClass.
	// +optional
	VolumeSnapshotClassName *string `json:"volumeSnapshotClassName,omitempty"`
}

// JobOptions customises the Jobs that move data.
type JobOptions struct {
	// Resources for the data mover container.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// SecurityContext replaces the default container security context.
	// By default the mover runs as root with only DAC_OVERRIDE (backup) or
	// CHOWN/DAC_OVERRIDE/FOWNER/FSETID (restore), which satisfies the "baseline" Pod Security Standard.
	// +optional
	SecurityContext *corev1.SecurityContext `json:"securityContext,omitempty"`

	// ServiceAccountName for the mover pod, e.g. one bound to an IAM role via IRSA.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// Tolerations added to the mover pod.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// NodeSelector for the mover pod. Ignored when the pod must run next to a workload holding a ReadWriteOnce volume.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// ActiveDeadlineSeconds bounds how long a single backup or restore may run.
	// +kubebuilder:default=21600
	// +kubebuilder:validation:Minimum=60
	// +optional
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`

	// BackoffLimit is the number of retries before the Job is marked failed.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=0
	// +optional
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`
}

// SourceVolume records the properties of the backed-up PVC so a restore can recreate it.
type SourceVolume struct {
	StorageClassName *string                             `json:"storageClassName,omitempty"`
	AccessModes      []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`
	Capacity         string                              `json:"capacity,omitempty"`
}
