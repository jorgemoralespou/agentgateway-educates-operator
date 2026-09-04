package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
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
// Their CRDs may not exist when the operator starts — the Gateway API ones
// arrive with this operator's Helm chart (ADR-0006) and agentgateway's arrive
// from the charts the platform installs — and a typed client caches a REST
// mapping at startup, so every typed call to a kind whose CRD appeared later
// fails until the pod restarts. Unstructured access with an explicit GVK
// sidesteps that entirely.

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

// newModelList is newModel for a List call. Models are the one kind here with
// no fixed name — a catalog names them — so both the catalog's prune and the
// platform's teardown have to find them by label rather than construct them.
func newModelList() *unstructured.UnstructuredList {
	l := &unstructured.UnstructuredList{}
	l.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   AgentgatewayGroup,
		Version: AgentgatewayVersion,
		Kind:    KindAgentgatewayModel + "List",
	})
	return l
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
// The overlay is a strategic-merge patch on the generated ServiceSpec. Creating
// it is only half the job: an overlay nothing references is inert, so the
// Gateway carries a spec.infrastructure.parametersRef pointing here. See
// ensureGatewayParametersRef.
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
	if err := r.pruneLegacyGateway(ctx, namespace); err != nil {
		return err
	}

	live := newGateway()
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: GatewayName}, live)
	if err == nil {
		// The Gateway exists. Its spec is not rewritten wholesale on every
		// pass: doing so would fight anyone who attached an extra listener, and
		// nothing in this operator's own configuration changes it. Two fields
		// are exceptions, both because a Gateway created by an earlier build is
		// broken without them — see ensureListenerRouteKinds and
		// ensureGatewayParametersRef.
		if err := r.ensureListenerRouteKinds(ctx, live); err != nil {
			return err
		}
		// Re-read: ensureListenerRouteKinds may have written, which makes the
		// copy above stale and the next Update a conflict.
		if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: GatewayName}, live); err != nil {
			return err
		}
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
		"infrastructure": map[string]any{
			"parametersRef": map[string]any{
				"group": AgentgatewayGroup,
				"kind":  KindAgentgatewayParameters,
				"name":  ParametersName,
			},
		},
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
					"kinds":      listenerRouteKinds(),
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

// listenerRouteKinds is what the listener accepts.
//
// AgentgatewayModel is strictly opt-in: agentgateway's default kinds for an
// HTTP listener are HTTPRoute and GRPCRoute, and a model attaching to a
// listener that does not name its kind is ignored in silence — no status on the
// model, no event, and the listener still reports Accepted and Programmed while
// attachedRoutes stays 0. Every request then 404s with "route not found" even
// though every resource looks healthy. Naming the kind is what enables the
// listener's built-in LLM paths, /v1/chat/completions and /v1/models.
//
// HTTPRoute is listed alongside it deliberately. Setting kinds at all narrows
// the listener to exactly what is listed, so omitting HTTPRoute would revoke
// the default. Nothing here creates one today, but a listener that silently
// stopped accepting them would be a trap for whoever adds the first.
func listenerRouteKinds() []any {
	return []any{
		map[string]any{
			"group": GatewayAPIGroup,
			"kind":  KindHTTPRoute,
		},
		map[string]any{
			"group": AgentgatewayGroup,
			"kind":  KindAgentgatewayModel,
		},
	}
}

// ensureListenerRouteKinds adds the model kind to a Gateway that predates it.
//
// The Gateway's spec is otherwise left alone once created, but this one field
// cannot be: a platform installed before this operator named the kind has a
// listener that ignores every model it renders, and nothing about that state
// self-corrects. Only allowedRoutes.kinds on the listener this operator owns is
// written; an extra listener someone else attached is untouched.
func (r *AgentGatewayPlatformReconciler) ensureListenerRouteKinds(ctx context.Context, gw *unstructured.Unstructured) error {
	listeners, found, err := unstructured.NestedSlice(gw.Object, "spec", "listeners")
	if err != nil || !found {
		return err
	}

	changed := false
	for i, raw := range listeners {
		listener, ok := raw.(map[string]any)
		if !ok || listener["name"] != GatewayListenerName {
			continue
		}

		allowed, _ := listener["allowedRoutes"].(map[string]any)
		if allowed == nil {
			allowed = map[string]any{}
		}
		if equality.Semantic.DeepEqual(allowed["kinds"], listenerRouteKinds()) {
			continue
		}
		allowed["kinds"] = listenerRouteKinds()
		if allowed["namespaces"] == nil {
			allowed["namespaces"] = map[string]any{"from": "All"}
		}
		listener["allowedRoutes"] = allowed
		listeners[i] = listener
		changed = true
	}

	if !changed {
		return nil
	}
	if err := unstructured.SetNestedSlice(gw.Object, listeners, "spec", "listeners"); err != nil {
		return err
	}
	return r.Update(ctx, gw)
}

// ensureGatewayParametersRef points an existing Gateway at the parameters
// overlay, so a Gateway created before this operator wrote the ref still gets
// ClusterIP.
//
// The ref goes on the Gateway rather than the GatewayClass because the
// GatewayClass is agentgateway's own object — its controller creates it after
// leader election (ADR-0005) — and writing a field into a resource another
// controller owns invites a write loop where each side reasserts its version.
// The Gateway is this operator's, so nothing contends for it. agentgateway
// applies GatewayClass overlays first and Gateway overlays second, so the
// Gateway-level ref also wins outright over anything set cluster-wide.
//
// Only the ref is written; the rest of the spec is left alone.
func (r *AgentGatewayPlatformReconciler) ensureGatewayParametersRef(ctx context.Context, gw *unstructured.Unstructured) error {
	desired := map[string]any{
		"group": AgentgatewayGroup,
		"kind":  KindAgentgatewayParameters,
		"name":  ParametersName,
	}

	current, found, err := unstructured.NestedMap(gw.Object, "spec", "infrastructure", "parametersRef")
	if err != nil {
		return err
	}
	if found && equality.Semantic.DeepEqual(current, desired) {
		return nil
	}

	if err := unstructured.SetNestedMap(gw.Object, desired,
		"spec", "infrastructure", "parametersRef"); err != nil {
		return err
	}
	return r.Update(ctx, gw)
}

// pruneLegacyGateway removes the Gateway an earlier build created under the
// name the control-plane Helm release also uses.
//
// That Gateway can never become Programmed — see LegacyGatewayName — and
// renaming a Kubernetes object means creating a new one, so the wedged original
// would otherwise stay behind logging a rejected apply forever. Deleting it
// takes its data-plane Deployment and Service with it and leaves the
// control-plane objects of the same name untouched, since those belong to the
// Helm release and were never owned by the Gateway.
//
// Guarded on this operator's own managed-by label: a cluster operator who
// hand-wrote a Gateway called "agentgateway", or who points spec.provider at an
// external one, keeps it.
func (r *AgentGatewayPlatformReconciler) pruneLegacyGateway(ctx context.Context, namespace string) error {
	if LegacyGatewayName == GatewayName {
		return nil
	}

	legacy := newGateway()
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: LegacyGatewayName}, legacy)
	if err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil
		}
		return err
	}

	if legacy.GetLabels()[ManagedByLabel] != ManagedByValue {
		return nil
	}

	if err := r.Delete(ctx, legacy); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete legacy Gateway %s/%s: %w", namespace, LegacyGatewayName, err)
	}
	return nil
}

// gatewayAPIPresent reports whether the Gateway API CRDs are established, by
// asking the REST mapper rather than reading CRD objects.
//
// A no-match is not taken at face value: the mapper caches, so it reports a
// miss both when the CRDs are genuinely absent and when they were installed
// after this mapper last looked. Those are indistinguishable from the error
// alone, so a miss resets the mapper and asks once more. Without that, the
// `gatewayAPI.install: false` path — where a cluster operator installs Gateway
// API themselves after this operator is already running — would sit in
// Installing until someone restarted the pod.
func (r *AgentGatewayPlatformReconciler) gatewayAPIPresent(_ context.Context) (bool, error) {
	gk := schema.GroupKind{Group: GatewayAPIGroup, Kind: KindGateway}

	_, err := r.RESTMapper().RESTMapping(gk, GatewayAPIVersion)
	if err == nil {
		return true, nil
	}
	if !meta.IsNoMatchError(err) {
		return false, err
	}

	resetter, ok := r.RESTMapper().(meta.ResettableRESTMapper)
	if !ok {
		return false, nil
	}
	resetter.Reset()

	if _, err := r.RESTMapper().RESTMapping(gk, GatewayAPIVersion); err != nil {
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
