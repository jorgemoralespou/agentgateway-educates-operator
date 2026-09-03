// Package v1alpha1 contains API Schema definitions for the agentgateway
// v1alpha1 API group.
// +kubebuilder:object:generate=true
// +groupName=agentgateway.operators.educates.dev
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// SchemeGroupVersion is group version used to register these objects.
	// This name is used by applyconfiguration generators (e.g. controller-gen).
	//
	// The `operators.` infix marks third-party operators as peers of the
	// platform, distinct from the runtime's training.educates.dev and the v4
	// installer's platform.educates.dev.
	SchemeGroupVersion = schema.GroupVersion{Group: "agentgateway.operators.educates.dev", Version: "v1alpha1"}

	// GroupVersion is an alias for SchemeGroupVersion, for backward compatibility.
	GroupVersion = SchemeGroupVersion

	// SchemeBuilder collects the functions that add this group-version's
	// types to a scheme. apimachinery-only — controller-runtime's
	// pkg/scheme.Builder is deprecated precisely because api packages
	// should not depend on controller-runtime.
	SchemeBuilder = runtime.NewSchemeBuilder()

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// register queues the given object types for registration under this
// group-version, mirroring what controller-runtime's deprecated
// scheme.Builder.Register did. Each *_types.go file calls it from
// init().
func register(objs ...runtime.Object) {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, objs...)
		metav1.AddToGroupVersion(s, SchemeGroupVersion)
		return nil
	})
}
