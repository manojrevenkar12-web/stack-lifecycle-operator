// Package v1alpha1 holds the PlatformStack API.
// +groupName=platform.stacklifecycle.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the API group and version of PlatformStack.
	GroupVersion = schema.GroupVersion{Group: "platform.stacklifecycle.io", Version: "v1alpha1"}

	// SchemeBuilder registers the types with a scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
