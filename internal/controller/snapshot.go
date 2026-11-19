package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/BardiaYaghmaie/bardiup/api/v1alpha1"
)

var volumeSnapshotGVK = schema.GroupVersionKind{Group: "snapshot.storage.k8s.io", Version: "v1", Kind: "VolumeSnapshot"}

func newVolumeSnapshot(name, namespace, pvc string, class *string, owner metav1.OwnerReference) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(volumeSnapshotGVK)
	u.SetName(name)
	u.SetNamespace(namespace)
	u.SetLabels(map[string]string{v1alpha1.LabelManagedBy: v1alpha1.ManagedByValue})
	u.SetOwnerReferences([]metav1.OwnerReference{owner})
	spec := map[string]any{"source": map[string]any{"persistentVolumeClaimName": pvc}}
	if class != nil && *class != "" {
		spec["volumeSnapshotClassName"] = *class
	}
	u.Object["spec"] = spec
	return u
}

type snapshotState struct {
	ready       bool
	err         string
	restoreSize *resource.Quantity
}

func getSnapshotState(ctx context.Context, r client.Reader, namespace, name string) (*snapshotState, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(volumeSnapshotGVK)
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, u); err != nil {
		return nil, err
	}
	st := &snapshotState{}
	st.ready, _, _ = unstructured.NestedBool(u.Object, "status", "readyToUse")
	st.err, _, _ = unstructured.NestedString(u.Object, "status", "error", "message")
	if s, ok, _ := unstructured.NestedString(u.Object, "status", "restoreSize"); ok {
		if q, err := resource.ParseQuantity(s); err == nil {
			st.restoreSize = &q
		}
	}
	return st, nil
}

// clonePVC builds the temporary PVC provisioned from a snapshot for the mover to read.
func clonePVC(name string, src *corev1.PersistentVolumeClaim, snapshot string, restoreSize *resource.Quantity, owner metav1.OwnerReference) *corev1.PersistentVolumeClaim {
	size := src.Spec.Resources.Requests[corev1.ResourceStorage]
	if restoreSize != nil && restoreSize.Cmp(size) > 0 {
		size = *restoreSize
	}
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       src.Namespace,
			Labels:          map[string]string{v1alpha1.LabelManagedBy: v1alpha1.ManagedByValue},
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: src.Spec.StorageClassName,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			VolumeMode:       ptr.To(corev1.PersistentVolumeFilesystem),
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: size}},
			DataSource: &corev1.TypedLocalObjectReference{
				APIGroup: ptr.To(volumeSnapshotGVK.Group),
				Kind:     volumeSnapshotGVK.Kind,
				Name:     snapshot,
			},
		},
	}
}

func snapshotNames(backup *v1alpha1.Backup) (snapshot, clone string) {
	base := boundedName(backup.Name, 50)
	return base + "-snap", base + "-clone"
}

func deleteSnapshotResources(ctx context.Context, c client.Client, backup *v1alpha1.Backup) error {
	if backup.Spec.Method != v1alpha1.BackupMethodSnapshot {
		return nil
	}
	snap, clone := snapshotNames(backup)
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: clone, Namespace: backup.Namespace}}
	if err := client.IgnoreNotFound(c.Delete(ctx, pvc)); err != nil {
		return fmt.Errorf("delete clone PVC: %w", err)
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(volumeSnapshotGVK)
	u.SetName(snap)
	u.SetNamespace(backup.Namespace)
	if err := c.Delete(ctx, u); err != nil && client.IgnoreNotFound(err) != nil && !meta.IsNoMatchError(err) {
		return fmt.Errorf("delete VolumeSnapshot: %w", err)
	}
	return nil
}
