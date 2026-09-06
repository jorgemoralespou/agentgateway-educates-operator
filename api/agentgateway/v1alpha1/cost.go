package v1alpha1

import (
	"fmt"
	"strings"
)

// Cost budgets are expressed in US dollars, the unit the provider invoices in,
// and enforced in micro-dollars.
//
// Micro-dollars because the protocol field carrying a descriptor's cost is an
// unsigned integer, so the value has to be a whole number, and because that is
// the grain agentgateway prices requests at internally. A coarser unit such as
// whole cents would round most individual requests to zero and enforce nothing.

// MicroDollarsPerDollar is the scaling factor between the authored unit and the
// enforced one.
const MicroDollarsPerDollar = 1_000_000

// costDecimalPlaces is how many fractional digits survive the conversion. Six
// is exactly micro-dollar grain.
const costDecimalPlaces = 6

// ParseDollarsToMicroDollars converts an authored dollar string to
// micro-dollars, exactly.
//
// Parsed from a string by hand rather than through a float: a floating-point
// field in a custom resource would admit representation errors into a value
// that is compared for equality and rendered into a CEL expression, so no
// float appears anywhere in this path.
//
// Digits beyond micro-dollar grain are truncated rather than rounded. At this
// grain the difference is a millionth of a dollar, and truncating never
// enforces a budget larger than the author asked for.
func ParseDollarsToMicroDollars(s string) (int64, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, fmt.Errorf("cost budget is empty")
	}
	// No sign is accepted at all. A negative budget has no meaning, and a
	// leading "+" would be the only thing distinguishing two spellings of one
	// value.
	if strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "+") {
		return 0, fmt.Errorf("cost budget %q must not carry a sign", s)
	}

	whole, frac, hasFrac := strings.Cut(trimmed, ".")
	if hasFrac && strings.Contains(frac, ".") {
		return 0, fmt.Errorf("cost budget %q has more than one decimal point", s)
	}
	// "5." and ".5" are both rejected: one of the two halves being empty is
	// more likely a truncated value than an intended one.
	if whole == "" || (hasFrac && frac == "") {
		return 0, fmt.Errorf("cost budget %q is not a decimal number", s)
	}

	// Truncate, then pad, so every input reaches exactly micro-dollar grain
	// before a single multiplication.
	if len(frac) > costDecimalPlaces {
		frac = frac[:costDecimalPlaces]
	}
	frac += strings.Repeat("0", costDecimalPlaces-len(frac))

	micros, err := parseDigits(whole + frac)
	if err != nil {
		return 0, fmt.Errorf("cost budget %q is not a decimal number", s)
	}
	if micros == 0 {
		return 0, fmt.Errorf("cost budget %q rounds to zero micro-dollars", s)
	}
	return micros, nil
}

// ResolvedCostBudget is the outcome of resolving one grant's cost budget.
type ResolvedCostBudget struct {
	// Configured reports whether any cost ceiling applies at all. False means
	// no cost descriptor is rendered and only the token budget enforces.
	Configured bool

	// MicroDollars is the ceiling actually enforced, in micro-dollars.
	MicroDollars int64

	// Clamped reports that the grant asked for more than the catalog allows.
	Clamped bool

	// Requested is what the grant asked for, in micro-dollars, when it was
	// clamped. Zero otherwise.
	Requested int64
}

// ResolveCostBudget decides the spend ceiling one grant is enforced at.
//
// The same precedence as token budgets: the grant's own value if it set one,
// else the catalog's default, then bounded by the catalog's maximum. Unlike
// token budgets there is no built-in constant, because no cost ceiling at all
// is the right default: this project maintains no pricing data and cannot
// invent a sensible figure for someone else's provider account.
//
// Unparseable values are treated as absent rather than as an error. Validation
// on the CRD is what rejects a malformed string; by the time a value reaches
// here, failing a running attendee's request over a field the API server
// accepted would be worse than falling back.
func ResolveCostBudget(grant, catalogDefault, catalogMax string) ResolvedCostBudget {
	resolved := ResolvedCostBudget{}

	if v, err := ParseDollarsToMicroDollars(grant); err == nil {
		resolved.Configured = true
		resolved.MicroDollars = v
	} else if v, err := ParseDollarsToMicroDollars(catalogDefault); err == nil {
		resolved.Configured = true
		resolved.MicroDollars = v
	}

	if !resolved.Configured {
		return resolved
	}

	// The maximum bounds the catalog's own default too, so an operator cannot
	// configure a default that exceeds their own ceiling.
	if max, err := ParseDollarsToMicroDollars(catalogMax); err == nil && resolved.MicroDollars > max {
		resolved.Requested = resolved.MicroDollars
		resolved.MicroDollars = max
		// Only a grant that asked for too much is reported as clamped, matching
		// how token budgets report it: a clamped default is the operator's own
		// two settings disagreeing, not something the author did.
		_, grantErr := ParseDollarsToMicroDollars(grant)
		resolved.Clamped = grantErr == nil
	}

	return resolved
}

// CatalogDefaultCostBudget is the cluster-wide cost default this catalog
// declares, empty when it declares none.
func (c *AgentGatewayCatalog) CatalogDefaultCostBudget() string {
	if c == nil || c.Spec.Budgets == nil {
		return ""
	}
	return c.Spec.Budgets.DefaultCostBudget
}

// MaxCostBudget is the spend ceiling this catalog imposes, empty when it
// imposes none.
func (c *AgentGatewayCatalog) MaxCostBudget() string {
	if c == nil || c.Spec.Budgets == nil {
		return ""
	}
	return c.Spec.Budgets.MaxCostBudget
}

// parseDigits reads an unsigned decimal, rejecting anything that is not a
// digit.
//
// strconv.ParseInt would accept a sign and underscores, both of which have
// already been ruled out above and neither of which should slip back in
// through the concatenated digits.
func parseDigits(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("no digits")
	}
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a digit: %q", r)
		}
		next := n*10 + int64(r-'0')
		// Overflow would silently enforce a budget unrelated to the one
		// written, so it is an error rather than a wrap.
		if next < n {
			return 0, fmt.Errorf("value out of range")
		}
		n = next
	}
	return n, nil
}
