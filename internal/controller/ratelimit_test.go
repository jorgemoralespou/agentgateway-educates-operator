package controller

import (
	"strings"
	"testing"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

// The descriptor row is what every grant that names no budget of its own is
// enforced at, so it is the mechanism by which a catalog edit reaches a running
// session. It is asserted directly rather than through a cluster.
func TestRenderRateLimitConfig(t *testing.T) {
	tests := []struct {
		name          string
		window        string
		defaultBudget int64
		wantContains  []string
	}{
		{
			name:          "carries the budget it was given",
			window:        "hour",
			defaultBudget: 40000,
			wantContains: []string{
				"domain: " + RateLimitDomain,
				"key: " + metadataKeySession,
				"unit: hour",
				"requests_per_unit: 40000",
			},
		},
		{
			name:          "carries the window it was given",
			window:        "day",
			defaultBudget: agentgatewayv1alpha1.DefaultTokenBudget,
			wantContains: []string{
				"unit: day",
				"requests_per_unit: 100000",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderRateLimitConfig(tt.window, tt.defaultBudget)
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("config missing %q:\n%s", want, got)
				}
			}
		})
	}
}

// The row and the policy's fallback must agree: a request whose registration
// carries no budget is limited by the row, and the two disagreeing would
// enforce one number while the policy claimed another.
func TestRateLimitRowMatchesPolicyFallback(t *testing.T) {
	catalog := &agentgatewayv1alpha1.AgentGatewayCatalog{
		Spec: agentgatewayv1alpha1.AgentGatewayCatalogSpec{
			Budgets: &agentgatewayv1alpha1.BudgetSpec{
				DefaultTokenBudget: agentgatewayv1alpha1.TokenBudgetValue(40000),
			},
		},
	}

	config := renderRateLimitConfig(string(agentgatewayv1alpha1.DefaultBudgetWindow), catalog.EffectiveDefaultTokenBudget())
	if !strings.Contains(config, "requests_per_unit: 40000") {
		t.Errorf("the descriptor row must carry the catalog's default:\n%s", config)
	}

	override := tokenBudgetOverride(string(agentgatewayv1alpha1.DefaultBudgetWindow), catalog.EffectiveDefaultTokenBudget())
	if !strings.Contains(override, "40000") {
		t.Errorf("the policy fallback must carry the same default: %s", override)
	}
}
