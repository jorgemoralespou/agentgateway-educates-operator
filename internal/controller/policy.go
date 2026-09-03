package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

// ensurePolicy renders the single API-key policy.
//
// Exactly one policy, cluster-wide. API-key policies *replace* rather than merge
// — two policies targeting one Gateway means one silently wins by creation
// order, with no error surfaced, and attendees would lose access
// non-deterministically (ADR-0002). That is why the name is a constant and
// nothing here is per-workshop.
func (r *AgentGatewayPlatformReconciler) ensurePolicy(ctx context.Context, platform *agentgatewayv1alpha1.AgentGatewayPlatform, namespace string) error {
	spec := renderPolicySpec(platform, namespace)

	live := newPolicy()
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: PolicyName}, live)
	if apierrors.IsNotFound(err) {
		desired := newPolicy()
		desired.SetName(PolicyName)
		desired.SetNamespace(namespace)
		desired.SetLabels(map[string]string{ManagedByLabel: ManagedByValue})
		if err := unstructured.SetNestedMap(desired.Object, spec, "spec"); err != nil {
			return err
		}
		if err := r.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create API-key policy: %w", err)
		}
		return nil
	}
	if err != nil {
		return err
	}

	if err := unstructured.SetNestedMap(live.Object, spec, "spec"); err != nil {
		return err
	}
	return r.Update(ctx, live)
}

// renderPolicySpec builds the policy that authenticates participant keys and
// enforces token budgets.
//
// Split out as a pure function so the descriptor shape — the part that is easy
// to get subtly wrong and expensive to debug on a cluster — is unit-testable.
func renderPolicySpec(platform *agentgatewayv1alpha1.AgentGatewayPlatform, namespace string) map[string]any {
	return map[string]any{
		// targetRefs has no namespace field: the target must be in the policy's
		// own namespace. That restriction is the whole reason registrations
		// cannot live beside the attendee's pod (ADR-0002).
		"targetRefs": []any{
			map[string]any{
				"group": GatewayAPIGroup,
				"kind":  KindGateway,
				"name":  GatewayName,
			},
		},
		"traffic": map[string]any{
			"apiKeyAuthentication": map[string]any{
				// Strict: an unauthenticated request is rejected rather than
				// passed through unmetered.
				"mode": "Strict",
				// Selects every registration ConfigMap by label. ConfigMap
				// sourcing requires keyHash rather than a raw key, which is the
				// constraint that keeps credentials out of the gateway
				// namespace entirely.
				"configMapSelector": map[string]any{
					"matchLabels": map[string]any{
						agentgatewayv1alpha1.RegistrationLabel: "true",
					},
				},
			},
			"rateLimit": map[string]any{
				"global": map[string]any{
					"domain": RateLimitDomain,
					// backendRef *does* take a namespace — the cross-namespace
					// restriction is specific to the credential selector.
					"backendRef": map[string]any{
						"group":     "",
						"kind":      "Service",
						"name":      RateLimitServiceName,
						"namespace": namespace,
						"port":      int64(RateLimitPort),
					},
					// Always written explicitly: the CRD declares no schema
					// default, so omitting it would leave the field absent and
					// leave the choice to the data plane (ADR-0003).
					"failureMode": string(platform.FailureMode()),
					"descriptors": []any{
						map[string]any{
							// Tokens, not Requests: one request with a large
							// context can cost more than a hundred small ones,
							// so request counts are a poor proxy for cost.
							// Note this is the descriptor's *cost* unit, not
							// the time unit on rateLimit.local.
							"unit": "Tokens",
							"entries": []any{
								map[string]any{
									"name": metadataKeySession,
									// Flattened form. agentgateway flattens
									// registration metadata onto the apiKey
									// object, so this is `apiKey.session` and
									// never `apiKey.metadata.session`
									// (ADR-0004).
									"expression": "apiKey." + metadataKeySession,
								},
							},
						},
					},
				},
			},
		},
	}
}
