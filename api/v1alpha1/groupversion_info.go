// Package v1alpha1 contains the API types of the continuum CRDs.
//
// +kubebuilder:object:generate=true
// +groupName=continuum.blinklabs.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/chrisguiney/cardano-continuum/internal/ident"
)

var (
	// GroupVersion is the API group and version of the continuum CRDs.
	GroupVersion = schema.GroupVersion{Group: ident.APIGroup, Version: "v1alpha1"}

	// SchemeBuilder registers the types of this group.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types of this group to a runtime.Scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&ContinuumNetwork{}, &ContinuumNetworkList{},
		&ContinuumPool{}, &ContinuumPoolList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
