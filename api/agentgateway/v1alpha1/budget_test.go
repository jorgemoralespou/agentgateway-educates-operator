package v1alpha1

import "testing"

// The resolution chain is the one place precedence between a grant, a catalog
// and the built-in constant is decided, so it is table-tested directly rather
// than only through a controller that would need an API server to exercise it.
func TestResolveTokenBudget(t *testing.T) {
	budget := func(v int64) *int64 { return &v }

	tests := []struct {
		name           string
		grant          *int64
		catalogDefault *int64
		catalogMax     *int64
		want           int64
		wantInherited  bool
		wantClamped    bool
		wantRequested  int64
	}{
		{
			name:           "a grant that sets a budget keeps it",
			grant:          budget(250000),
			catalogDefault: budget(50000),
			want:           250000,
			wantInherited:  false,
		},
		{
			name:           "an unset grant takes the catalog default",
			grant:          nil,
			catalogDefault: budget(50000),
			want:           50000,
			wantInherited:  true,
		},
		{
			name:           "an unset grant with no catalog default takes the built-in constant",
			grant:          nil,
			catalogDefault: nil,
			want:           DefaultTokenBudget,
			wantInherited:  true,
		},
		{
			// Zero is rejected by validation, so reaching here means a client
			// stripped the field. Treated as absent rather than as a ceiling of
			// no tokens at all.
			name:           "a zero grant is treated as unset",
			grant:          budget(0),
			catalogDefault: budget(50000),
			want:           50000,
			wantInherited:  true,
		},
		{
			name:           "a grant set to the same value as the catalog is still not inherited",
			grant:          budget(50000),
			catalogDefault: budget(50000),
			want:           50000,
			wantInherited:  false,
		},
		{
			name:          "a grant above the maximum is clamped to it",
			grant:         budget(900000),
			catalogMax:    budget(200000),
			want:          200000,
			wantClamped:   true,
			wantRequested: 900000,
		},
		{
			name:       "a grant exactly at the maximum is left alone",
			grant:      budget(200000),
			catalogMax: budget(200000),
			want:       200000,
		},
		{
			name:       "a grant below the maximum is left alone",
			grant:      budget(10000),
			catalogMax: budget(200000),
			want:       10000,
		},
		{
			name:       "no maximum configured clamps nothing",
			grant:      budget(900000),
			catalogMax: nil,
			want:       900000,
		},
		{
			// An operator cannot configure a default that exceeds the ceiling
			// they set for themselves.
			name:           "the catalog default is itself bounded by the maximum",
			grant:          nil,
			catalogDefault: budget(500000),
			catalogMax:     budget(200000),
			want:           200000,
			wantInherited:  true,
			// Not the author's doing, so not reported as their value being
			// clamped.
			wantClamped: false,
		},
		{
			// Same for the built-in constant, when a maximum is set below it.
			name:          "the built-in constant is bounded by the maximum",
			grant:         nil,
			catalogMax:    budget(1000),
			want:          1000,
			wantInherited: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveTokenBudget(tt.grant, tt.catalogDefault, tt.catalogMax)
			if got.Value != tt.want {
				t.Errorf("Value = %d, want %d", got.Value, tt.want)
			}
			if got.Inherited != tt.wantInherited {
				t.Errorf("Inherited = %v, want %v", got.Inherited, tt.wantInherited)
			}
			if got.Clamped != tt.wantClamped {
				t.Errorf("Clamped = %v, want %v", got.Clamped, tt.wantClamped)
			}
			if tt.wantClamped && got.Requested != tt.wantRequested {
				t.Errorf("Requested = %d, want %d", got.Requested, tt.wantRequested)
			}
		})
	}
}

// Ticket 04 requires that changing the catalog's default reaches an inheriting
// grant "without any grant being reconciled", and states plainly that the
// mechanism is "no fan-out watch across grants". Ticket 05 then requires that
// a lowered maximum reaches a grant that pinned its own value, which nothing
// but a reconcile can do. Both hold only because the two populations are
// disjoint, so this is the predicate that keeps them apart.
func TestNeedsReconcileOnCatalogChange(t *testing.T) {
	tests := []struct {
		name              string
		spec              AgentGatewaySessionSpec
		catalogOffersCost bool
		want              bool
	}{
		{
			// The property ticket 04 rests on: an inheriting grant follows the
			// shared descriptor row, so waking it would be pure cost.
			name: "a grant inheriting everything is left alone",
			spec: AgentGatewaySessionSpec{},
			want: false,
		},
		{
			// The property ticket 05 rests on: a pinned budget bypasses that
			// row, so a lowered maximum reaches it only by reconciling.
			name: "a grant pinning a token budget is woken",
			spec: AgentGatewaySessionSpec{TokenBudget: TokenBudgetValue(50000)},
			want: true,
		},
		{
			name: "a grant pinning a cost budget is woken",
			spec: AgentGatewaySessionSpec{CostBudget: "0.50"},
			want: true,
		},
		{
			// A cost budget lands on the registration even when inherited,
			// because the cost descriptor keys on the field's presence and so
			// has no shared row to fall through to.
			name:              "an inheriting grant is woken when the catalog offers a cost budget",
			spec:              AgentGatewaySessionSpec{},
			catalogOffersCost: true,
			want:              true,
		},
		{
			// Zero is rejected by validation; reaching here it means a client
			// stripped the field, which is an inheriting grant.
			name: "a stripped zero token budget counts as inheriting",
			spec: AgentGatewaySessionSpec{TokenBudget: TokenBudgetValue(0)},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := &AgentGatewaySession{Spec: tt.spec}
			if got := session.NeedsReconcileOnCatalogChange(tt.catalogOffersCost); got != tt.want {
				t.Errorf("NeedsReconcileOnCatalogChange(%v) = %v, want %v",
					tt.catalogOffersCost, got, tt.want)
			}
		})
	}
}

func TestEffectiveDefaultTokenBudget(t *testing.T) {
	budget := func(v int64) *int64 { return &v }

	tests := []struct {
		name    string
		catalog *AgentGatewayCatalog
		want    int64
	}{
		{
			name:    "a catalog with no budget block falls back to the constant",
			catalog: &AgentGatewayCatalog{},
			want:    DefaultTokenBudget,
		},
		{
			name: "a catalog with an empty budget block falls back to the constant",
			catalog: &AgentGatewayCatalog{
				Spec: AgentGatewayCatalogSpec{Budgets: &BudgetSpec{}},
			},
			want: DefaultTokenBudget,
		},
		{
			name: "a catalog default is used when set",
			catalog: &AgentGatewayCatalog{
				Spec: AgentGatewayCatalogSpec{
					Budgets: &BudgetSpec{DefaultTokenBudget: budget(30000)},
				},
			},
			want: 30000,
		},
		{
			// The row every inheriting grant is enforced at must respect the
			// operator's own ceiling too.
			name: "a catalog default above the maximum is bounded by it",
			catalog: &AgentGatewayCatalog{
				Spec: AgentGatewayCatalogSpec{
					Budgets: &BudgetSpec{
						DefaultTokenBudget: budget(500000),
						MaxTokenBudget:     budget(200000),
					},
				},
			},
			want: 200000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.catalog.EffectiveDefaultTokenBudget(); got != tt.want {
				t.Errorf("EffectiveDefaultTokenBudget() = %d, want %d", got, tt.want)
			}
		})
	}
}
