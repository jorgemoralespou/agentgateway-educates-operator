package controller

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// EducatesClusterConfig is the v4 installer's own singleton, which this
// operator reads but never writes.
//
// Handled as unstructured with an explicit GVK rather than by importing the
// installer's types: this operator is a peer of educates-installer, not a
// plugin, and taking a Go dependency on it would couple their release cycles
// for the sake of reading two status fields. The CRD may also be absent
// entirely, on a cluster where this operator is installed without Educates.
const (
	educatesConfigGroup   = "config.educates.dev"
	educatesConfigVersion = "v1alpha1"
	educatesConfigKind    = "EducatesClusterConfig"

	// educatesConfigName is the installer's singleton, named `cluster` the same
	// way this operator's own singletons are.
	educatesConfigName = "cluster"
)

func newEducatesClusterConfig() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   educatesConfigGroup,
		Version: educatesConfigVersion,
		Kind:    educatesConfigKind,
	})
	return u
}

// clusterConfigState is what this operator needs to know about the platform it
// is running alongside.
type clusterConfigState struct {
	// Present is false when the CRD or the resource does not exist. Not an
	// error: this operator is usable on a cluster without Educates, and during
	// an install the config may simply not be there yet.
	Present bool

	// Ready reports whether the installer has published a Ready status.
	Ready bool

	// Message explains the state, for a condition.
	Message string
}

// clusterConfigStatus reads EducatesClusterConfig's status.
//
// Reads `.status` and never `.spec`, per the v4 contract: the spec is what a
// cluster operator asked for, the status is what the installer actually
// achieved, and only the latter is safe for another component to build on.
func clusterConfigStatus(ctx context.Context, c client.Client) (clusterConfigState, error) {
	config := newEducatesClusterConfig()

	err := c.Get(ctx, types.NamespacedName{Name: educatesConfigName}, config)
	if err != nil {
		if meta.IsNoMatchError(err) {
			// Educates is not installed on this cluster at all. Supported: the
			// gateway works without it, and a workshop author simply has no
			// Educates to integrate with.
			return clusterConfigState{
				Present: false,
				Message: "no EducatesClusterConfig CRD on this cluster; running without Educates integration",
			}, nil
		}
		if apierrors.IsNotFound(err) {
			return clusterConfigState{
				Present: false,
				Message: "no EducatesClusterConfig named 'cluster' exists yet",
			}, nil
		}
		return clusterConfigState{}, err
	}

	conditions, found, err := unstructured.NestedSlice(config.Object, "status", "conditions")
	if err != nil {
		return clusterConfigState{}, err
	}
	if !found {
		return clusterConfigState{
			Present: true,
			Ready:   false,
			Message: "EducatesClusterConfig has not published a status yet",
		}, nil
	}

	for _, raw := range conditions {
		cond, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if cond["type"] != "Ready" {
			continue
		}
		if cond["status"] == "True" {
			return clusterConfigState{
				Present: true,
				Ready:   true,
				Message: "EducatesClusterConfig is ready",
			}, nil
		}
		message, _ := cond["message"].(string)
		if message == "" {
			message = "EducatesClusterConfig is not ready"
		}
		return clusterConfigState{Present: true, Ready: false, Message: message}, nil
	}

	return clusterConfigState{
		Present: true,
		Ready:   false,
		Message: "EducatesClusterConfig has published no Ready condition",
	}, nil
}
