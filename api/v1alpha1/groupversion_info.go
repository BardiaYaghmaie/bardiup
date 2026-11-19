// Package v1alpha1 contains API Schema definitions for the bardiup v1alpha1 API group.
// +kubebuilder:object:generate=true
// +groupName=bardiup.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion is the API group and version of Bardiup resources.
var GroupVersion = schema.GroupVersion{Group: "bardiup.io", Version: "v1alpha1"}

// AddToScheme registers Bardiup types.
func AddToScheme(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &BackupConfig{}, &BackupConfigList{}, &Backup{}, &BackupList{}, &Restore{}, &RestoreList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
