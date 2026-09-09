package controller

import (
	"strings"
	"testing"

	agentgatewayv1alpha1 "github.com/educates/agentgateway-educates-operator/api/agentgateway/v1alpha1"
)

// The condition message is what an author reads when their requested budget did
// not survive, so a micro-dollar figure has to render back as the dollars they
// wrote.
func TestFormatMicroDollars(t *testing.T) {
	tests := []struct {
		name   string
		micros int64
		want   string
	}{
		{name: "a whole dollar", micros: 1_000_000, want: "$1.00"},
		{name: "fifty cents", micros: 500_000, want: "$0.50"},
		{name: "a value with cents", micros: 12_340_000, want: "$12.34"},
		{name: "zero", micros: 0, want: "$0.00"},
		{name: "a sub-cent value keeps its precision", micros: 100, want: "$0.0001"},
		{name: "the smallest representable value", micros: 1, want: "$0.000001"},
		{name: "a value with an odd tail", micros: 1_234_567, want: "$1.234567"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatMicroDollars(tt.micros); got != tt.want {
				t.Errorf("formatMicroDollars(%d) = %q, want %q", tt.micros, got, tt.want)
			}
		})
	}
}

// A round trip, because the two halves are written independently and a mismatch
// would show up as a condition naming a figure the author never wrote.
func TestMicroDollarsRoundTrip(t *testing.T) {
	for _, in := range []string{"0.50", "1", "12.34", "0.0001", "2.5"} {
		micros, err := agentgatewayv1alpha1.ParseDollarsToMicroDollars(in)
		if err != nil {
			t.Fatalf("ParseDollarsToMicroDollars(%q) error: %v", in, err)
		}
		formatted := formatMicroDollars(micros)

		back, err := agentgatewayv1alpha1.ParseDollarsToMicroDollars(strings.TrimPrefix(formatted, "$"))
		if err != nil {
			t.Fatalf("re-parsing %q error: %v", formatted, err)
		}
		if back != micros {
			t.Errorf("round trip of %q: %d micro-dollars became %q, then %d", in, micros, formatted, back)
		}
	}
}

// The cost expression's presence test is load-bearing: agentgateway skips a
// descriptor whose cost expression fails to evaluate, logging at debug level
// only, and there is no feedback path back to this operator. A skipped
// descriptor is a budget silently not enforced.
func TestCostExpressionGuardsAgainstUnpricedRequests(t *testing.T) {
	expr := costExpression()

	// The exact field name matters and is not guessable: agentgateway's LLM CEL
	// context is camelCase and groups the realized cost under `llm.cost`, which
	// is itself absent for a model that could not be priced. An expression
	// naming a field that does not exist is an evaluation error rather than a
	// false has(), so the descriptor is dropped and the budget silently stops
	// being enforced. Verified against v1.5.0 on a cluster.
	if !strings.Contains(expr, "has(llm.cost)") {
		t.Errorf("the cost expression must test for a priced cost before using it: %s", expr)
	}
	if !strings.Contains(expr, "llm.cost.total") {
		t.Errorf("the cost expression must read the realized cost from llm.cost.total: %s", expr)
	}
	if strings.Contains(expr, "llm.total_cost") {
		t.Errorf("llm.total_cost does not exist in agentgateway v1.5.0: %s", expr)
	}
	// The fallback must be a positive flat charge. Charging nothing for what
	// cannot be priced turns an unpriced model into an unmetered one, which is
	// the failure this feature exists to prevent.
	if !strings.Contains(expr, "1000") {
		t.Errorf("the cost expression must charge a pessimistic fallback: %s", expr)
	}
	if unpricedRequestMicroDollars <= 0 {
		t.Errorf("the unpriced fallback must be positive, got %d", unpricedRequestMicroDollars)
	}
	// The realized cost is scaled to micro-dollars, matching what the budget is
	// stored in.
	if !strings.Contains(expr, "1000000") {
		t.Errorf("the cost expression must scale dollars to micro-dollars: %s", expr)
	}
}
