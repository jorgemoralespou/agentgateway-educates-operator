package controller

import (
	"context"
	"fmt"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

// tokenBudgetOverride reads each attendee's own token ceiling off their key
// registration.
//
// agentgateway's limitOverride must evaluate to an object with `unit` and
// `requestsPerUnit`. Combined with the descriptor's `unit: Tokens`, the count
// being limited is tokens rather than requests.
//
// The window and the fallback are arguments rather than baked into the text:
// the window is a per-grant choice and the fallback tracks whatever the catalog
// or the built-in constant says the ordinary budget is. Hardcoding either put
// two copies of one value in the tree, free to drift apart silently.
//
// Registration metadata is map[string]string, so the value is parsed back to an
// integer here. A registration written by an older operator may not carry the
// field at all, and a grant that inherits its budget deliberately omits it, so
// a missing or unparseable value falls back rather than failing the request.
//
// The window is read off the registration too, so a per-grant window reaches
// enforcement: the policy is one object cluster-wide and cannot hold a row per
// attendee. A registration written by an older operator carries no window, so a
// missing value falls back to the rendered default rather than producing an
// empty unit the rate-limit service would reject.
func tokenBudgetOverride(window string, fallbackBudget int64) string {
	unit := `(has(apiKey.` + metadataKeyBudgetWindow + `) ? ` +
		`apiKey.` + metadataKeyBudgetWindow + ` : "` + window + `")`
	return `has(apiKey.tokenBudget) ? ` +
		`{"unit": ` + unit + `, "requestsPerUnit": int(apiKey.tokenBudget)} : ` +
		`{"unit": ` + unit + `, "requestsPerUnit": ` + strconv.FormatInt(fallbackBudget, 10) + `}`
}

// costBudgetOverride reads each attendee's own spend ceiling off their key
// registration, in micro-dollars.
//
// Shares the token descriptor's window, so the two ceilings always cover the
// same span. A registration with no cost budget is not reached: the descriptor
// entry keys on the cost budget's presence, so a key without one contributes no
// entry and the descriptor does not apply to it.
// Guarded with has() like the token override, even though the descriptor's
// entry already keys on the field's presence. An override that failed to
// evaluate would fall back to the shared descriptor row, which is written in
// tokens rather than micro-dollars and would enforce a ceiling unrelated to any
// budget. The guard costs nothing and removes that coupling.
func costBudgetOverride(window string) string {
	unit := `(has(apiKey.` + metadataKeyBudgetWindow + `) ? ` +
		`apiKey.` + metadataKeyBudgetWindow + ` : "` + window + `")`
	return `has(apiKey.` + metadataKeyCostBudget + `) ? ` +
		`{"unit": ` + unit + `, "requestsPerUnit": int(apiKey.` + metadataKeyCostBudget + `)} : ` +
		// Unreachable while the entry keys on presence. If it ever is reached,
		// the smallest possible ceiling is the safe direction: an unmetered
		// attendee is the failure this feature exists to prevent.
		`{"unit": ` + unit + `, "requestsPerUnit": 1}`
}

// unpricedRequestMicroDollars is charged against a cost budget when a request
// could not be priced.
//
// agentgateway prices each request against its own built-in model cost catalog
// and exposes the realized dollar cost to CEL, but it states plainly that a
// request is not charged when its provider does not report the cost the budget
// unit needs. Unpriced models are therefore a real case, not a hypothetical.
//
// The fallback is deliberately pessimistic rather than zero. Charging nothing
// for what cannot be priced turns an unpriced model into an unmetered one,
// which is the failure this feature exists to prevent. A tenth of a cent per
// request is high enough that a runaway loop still exhausts a small budget, and
// low enough that a workshop on a priced model never notices it.
const unpricedRequestMicroDollars = 1000

// costExpression is what each request charges against the cost budget.
//
// The presence test is load-bearing. agentgateway skips a descriptor whose cost
// expression fails to evaluate or does not yield a non-negative integer, logging
// at debug level only. That is not covered by the rate-limit failure mode, which
// addresses the service being unreachable rather than a descriptor being
// dropped, and there is no feedback path back to this operator: a skipped
// descriptor is a budget silently not enforced. So the expression tests for a
// priced cost before using it and charges the pessimistic flat fallback
// otherwise, rather than risking an evaluation failure.
func costExpression() string {
	return `has(llm.total_cost) ? ` +
		`int(llm.total_cost * ` + strconv.Itoa(MicroDollarsPerDollar) + `) : ` +
		strconv.Itoa(unpricedRequestMicroDollars)
}

// MicroDollarsPerDollar mirrors the API package's constant, so the CEL
// expression and the parsed budget scale by the same factor.
const MicroDollarsPerDollar = agentgatewayv1alpha1.MicroDollarsPerDollar

// ensurePolicy renders the single API-key policy.
//
// Exactly one policy, cluster-wide. API-key policies *replace* rather than
// merge: two policies targeting one Gateway means one silently wins by creation
// order, with no error surfaced, and attendees would lose access
// non-deterministically (ADR-0002). That is why the name is a constant and
// nothing here is per-workshop.
func (r *AgentGatewayPlatformReconciler) ensurePolicy(ctx context.Context, namespace string) error {
	// The failure mode is a catalog setting, because it is a choice about
	// serving LLM traffic rather than about installing a gateway. The policy is
	// rendered here because it must be exactly one object and the platform owns
	// the gateway namespace, so the value is read across rather than passed in.
	//
	// A catalog that does not exist yet leaves the default in place; the catalog
	// controller re-renders the policy when it becomes ready.
	failureMode := agentgatewayv1alpha1.FailClosed
	requestTimeout := ""
	// The fallback traces to the shared constant rather than a literal, so the
	// value the policy falls back to and the value the accessor defaults to
	// cannot drift apart.
	fallbackBudget := agentgatewayv1alpha1.DefaultTokenBudget
	catalog := &agentgatewayv1alpha1.AgentGatewayCatalog{}
	if err := r.Get(ctx, types.NamespacedName{Name: agentgatewayv1alpha1.SingletonName}, catalog); err == nil {
		failureMode = catalog.FailureMode()
		requestTimeout = catalog.Spec.RequestTimeout
		// Kept equal to the rate-limit service's descriptor row: a request
		// whose registration carries no budget is limited by that row, and the
		// two disagreeing would enforce one number and report another.
		fallbackBudget = catalog.EffectiveDefaultTokenBudget()
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	spec := renderPolicySpec(failureMode, namespace, requestTimeout,
		string(agentgatewayv1alpha1.DefaultBudgetWindow), fallbackBudget)

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
// Split out as a pure function so the descriptor shape: the part that is easy
// to get subtly wrong and expensive to debug on a cluster, is unit-testable.
//
// budgetWindow and fallbackBudget parameterize the limit override. They are
// passed rather than read here because this function must stay pure, and
// because the values come from a catalog the caller has already fetched.
func renderPolicySpec(failureMode agentgatewayv1alpha1.RateLimitFailureMode, namespace, requestTimeout string, budgetWindow string, fallbackBudget int64) map[string]any {
	spec := map[string]any{
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
					// backendRef *does* take a namespace: the cross-namespace
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
					"failureMode": string(failureMode),
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
							// The per-session ceiling, read back off the
							// attendee's own registration.
							//
							// This is what makes the budget per-attendee rather
							// than per-cluster: one shared rate-limit config
							// cannot hold a row per session, but each key
							// carries its own limit and CEL reads it here
							// (ADR-0003).
							"limitOverride": tokenBudgetOverride(budgetWindow, fallbackBudget),
						},
						// The cost descriptor, beside the token one rather than
						// instead of it. Whichever ceiling runs out first stops
						// the attendee.
						//
						// The token descriptor stays because it cannot be
						// skipped: its cost defaults to the total token count
						// with no CEL involved, which makes it the backstop
						// when a cost expression fails silently.
						map[string]any{
							"unit": "Tokens",
							"entries": []any{
								map[string]any{
									"name":       metadataKeySession,
									"expression": "apiKey." + metadataKeySession,
								},
								// Keys on the cost budget's presence, so a key
								// without one contributes no entry and this
								// descriptor simply does not apply to it. That
								// is what makes the cost ceiling opt-in without
								// rendering a second policy.
								map[string]any{
									"name":       metadataKeyCostBudget,
									"expression": "apiKey." + metadataKeyCostBudget,
								},
							},
							// What each request charges. Unlike the token
							// descriptor, which defaults to the token count,
							// this one is an explicit expression.
							"cost":          costExpression(),
							"limitOverride": costBudgetOverride(budgetWindow),
						},
					},
				},
			},
		},
	}

	// Only when asked for. Writing agentgateway's own default back at it would
	// pin a value this operator has no opinion about, and would need changing
	// every time that default moved.
	if requestTimeout != "" {
		traffic, _ := spec["traffic"].(map[string]any)
		traffic["timeouts"] = map[string]any{
			"request": requestTimeout,
		}
	}

	return spec
}
