package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

// Gateway API and agentgateway resources are handled as unstructured objects
// rather than typed ones.
//
// Their CRDs may not exist when the operator starts — this operator installs
// them — and a typed client caches a REST mapping at startup, so every typed
// call to a kind whose CRD appeared later fails until the pod restarts.
// Unstructured access with an explicit GVK sidesteps that entirely.

func newGatewayClass() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   GatewayAPIGroup,
		Version: GatewayAPIVersion,
		Kind:    KindGatewayClass,
	})
	return u
}

func newGateway() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   GatewayAPIGroup,
		Version: GatewayAPIVersion,
		Kind:    KindGateway,
	})
	return u
}

func newParameters() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   AgentgatewayGroup,
		Version: AgentgatewayVersion,
		Kind:    KindAgentgatewayParameters,
	})
	return u
}

func newPolicy() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   AgentgatewayGroup,
		Version: AgentgatewayVersion,
		Kind:    KindAgentgatewayPolicy,
	})
	return u
}

func newModel() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   AgentgatewayGroup,
		Version: AgentgatewayVersion,
		Kind:    KindAgentgatewayModel,
	})
	return u
}

// unstructuredConditionTrue reads a status condition from an unstructured
// object.
//
// Used for Gateway's Programmed and GatewayClass's Accepted, both of which are
// standard Gateway API conditions.
func unstructuredConditionTrue(u *unstructured.Unstructured, condType string) bool {
	conditions, found, err := unstructured.NestedSlice(u.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}
	for _, raw := range conditions {
		cond, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if cond["type"] == condType {
			return cond["status"] == string(metav1.ConditionTrue)
		}
	}
	return false
}

// ensureParameters creates or updates the AgentgatewayParameters overlay that
// forces the data-plane Service to ClusterIP.
//
// agentgateway's own default is LoadBalancer, which never gets an address on a
// cluster with no load-balancer provider. Sessions reach the gateway over
// in-cluster DNS and nothing external ever connects to it, so this is not a
// user-facing field — a knob whose default is correct everywhere is surface
// without a decision behind it (ADR-0005).
//
// The overlay is a strategic-merge patch on the generated ServiceSpec and is
// retroactive: a Gateway created before the parameters existed is
// re-reconciled to ClusterIP.
func (r *AgentGatewayPlatformReconciler) ensureParameters(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform, namespace string) error {
	desired := newParameters()
	desired.SetName(ParametersName)
	desired.SetNamespace(namespace)
	desired.SetLabels(map[string]string{ManagedByLabel: ManagedByValue})
	if err := unstructured.SetNestedField(desired.Object,
		"ClusterIP", "spec", "service", "spec", "type"); err != nil {
		return err
	}

	live := newParameters()
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ParametersName}, live)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.Create(ctx, desired)
		}
		return err
	}

	// Only the field this operator owns is written, so a cluster operator who
	// has added their own overlay fields keeps them.
	if err := unstructured.SetNestedField(live.Object,
		"ClusterIP", "spec", "service", "spec", "type"); err != nil {
		return err
	}
	return r.Update(ctx, live)
}

// ensureGateway creates the Gateway, which is what causes agentgateway to
// provision data-plane pods.
//
// The GatewayClass is referenced but never created here: agentgateway's own
// controller creates it, after leader election.
func (r *AgentGatewayPlatformReconciler) ensureGateway(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform, namespace string) error {
	live := newGateway()
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: GatewayName}, live)
	if err == nil {
		// The Gateway exists. Its spec is not rewritten on every pass: doing so
		// would fight anyone who attached an extra listener, and nothing in this
		// operator's own configuration changes it.
		return r.ensureGatewayParametersRef(ctx, live)
	}
	if !apierrors.IsNotFound(err) {
		return err
	}

	gw := newGateway()
	gw.SetName(GatewayName)
	gw.SetNamespace(namespace)
	gw.SetLabels(map[string]string{ManagedByLabel: ManagedByValue})

	spec := map[string]any{
		"gatewayClassName": GatewayClassName,
		"listeners": []any{
			map[string]any{
				"name":     GatewayListenerName,
				"port":     int64(GatewayPort),
				"protocol": "HTTP",
				// Routes may attach from any namespace: models are rendered
				// into the gateway namespace, but leaving this open avoids a
				// second change if per-workshop routes are ever added.
				"allowedRoutes": map[string]any{
					"namespaces": map[string]any{"from": "All"},
				},
			},
		},
	}
	if err := unstructured.SetNestedMap(gw.Object, spec, "spec"); err != nil {
		return err
	}

	if err := r.Create(ctx, gw); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create Gateway %s/%s: %w", namespace, GatewayName, err)
	}
	return nil
}

// ensureGatewayParametersRef is a no-op placeholder for per-Gateway parameters.
//
// The ClusterIP overlay is attached at the GatewayClass level, which applies
// cluster-wide and retroactively, so nothing needs to be written onto the
// Gateway itself. Kept as a seam because agentgateway applies GatewayClass
// overlays before Gateway ones, so a per-Gateway override remains possible
// later without restructuring.
func (r *AgentGatewayPlatformReconciler) ensureGatewayParametersRef(_ context.Context, _ *unstructured.Unstructured) error {
	return nil
}

// gatewayAPIPresent reports whether the Gateway API CRDs are established, by
// asking the REST mapper rather than reading CRD objects.
func (r *AgentGatewayPlatformReconciler) gatewayAPIPresent(ctx context.Context) (bool, error) {
	_, err := r.RESTMapper().RESTMapping(schema.GroupKind{
		Group: GatewayAPIGroup,
		Kind:  KindGateway,
	}, GatewayAPIVersion)
	if err != nil {
		if meta.IsNoMatchError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// deleteIfPresent deletes an object, tolerating both a missing object and a
// missing kind.
//
// Both cases are normal during teardown: EducatesClusterConfig's own teardown
// guard hardcodes the three platform component kinds and will not wait for this
// operator's resources, so cluster services — and the CRDs defining these
// kinds — can be removed while this operator is still draining (ADR-0005).
func (r *AgentGatewayPlatformReconciler) deleteIfPresent(ctx context.Context, obj client.Object) error {
	if err := r.Delete(ctx, obj); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) || apierrors.IsMethodNotSupported(err) {
			return nil
		}
		// A CRD that has been removed surfaces as a no-kind-match, which the
		// typed client reports as a *meta.NoKindMatchError wrapped in a
		// discovery failure. Both are tolerated above.
		return err
	}
	return nil
}
