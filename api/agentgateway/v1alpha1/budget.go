package v1alpha1

// The budget resolution chain.
//
// One pure function, deliberately not a method on either object: it is the
// single place the precedence between a grant, a catalog and the built-in
// constant is decided, and both the controller writing a registration and the
// status it reports have to agree on the answer. Two implementations that
// almost agree would surface as an attendee hitting a ceiling their grant does
// not mention.

// ResolvedTokenBudget is the outcome of resolving one grant's token budget.
type ResolvedTokenBudget struct {
	// Value is the ceiling actually enforced.
	Value int64

	// Inherited reports that the grant asked for nothing and took the
	// cluster-wide value. The registration omits the budget metadata in that
	// case, so the gateway falls through to the shared descriptor row and a
	// later edit of the catalog reaches a running session.
	Inherited bool

	// Clamped reports that the grant asked for more than the catalog allows and
	// was brought down to the maximum. Surfaced on status so an author can see
	// their requested value did not survive.
	Clamped bool

	// Requested is what the grant asked for, when it was clamped. Zero
	// otherwise. Reported alongside Clamped so the message can name both
	// numbers.
	Requested int64
}

// ResolveTokenBudget decides the ceiling one grant is enforced at.
//
// Precedence: the grant's own value if it set one, else the catalog's default,
// else the built-in constant. The catalog's maximum then bounds whatever that
// produced. Every argument is nil-able because "unset" is a meaningful state at
// each link: it is what lets an omitted budget inherit rather than freeze
// whatever the default happened to be when the grant was written, and what
// keeps the maximum opt-in.
func ResolveTokenBudget(grant *int64, catalogDefault *int64, catalogMax *int64) ResolvedTokenBudget {
	resolved := ResolvedTokenBudget{}

	switch {
	case grant != nil && *grant > 0:
		resolved.Value = *grant
	case catalogDefault != nil && *catalogDefault > 0:
		// Inherited: the grant asked for nothing, so the registration carries
		// no budget and the shared descriptor row decides. Whether that row
		// came from the catalog or from the built-in constant is invisible to
		// the grant, which is what makes a later catalog edit take effect.
		resolved.Value = *catalogDefault
		resolved.Inherited = true
	default:
		resolved.Value = DefaultTokenBudget
		resolved.Inherited = true
	}

	// The maximum bounds the catalog's own default too, so an operator cannot
	// configure a default that exceeds the ceiling they set for themselves.
	if catalogMax != nil && *catalogMax > 0 && resolved.Value > *catalogMax {
		resolved.Requested = resolved.Value
		resolved.Value = *catalogMax
		// Only a grant that asked for too much is reported as clamped. A
		// clamped *default* is the operator's own two settings disagreeing,
		// not something the workshop author did, and the grant still inherits.
		resolved.Clamped = !resolved.Inherited
	}

	return resolved
}

// CatalogDefaultTokenBudget is the cluster-wide default this catalog declares,
// or nil when it declares none.
//
// Nil rather than the built-in constant so callers can still tell "the operator
// chose this" from "nobody chose anything", which ResolveTokenBudget needs.
func (c *AgentGatewayCatalog) CatalogDefaultTokenBudget() *int64 {
	if c == nil || c.Spec.Budgets == nil {
		return nil
	}
	return c.Spec.Budgets.DefaultTokenBudget
}

// MaxTokenBudget is the ceiling this catalog imposes, or nil when it imposes
// none.
func (c *AgentGatewayCatalog) MaxTokenBudget() *int64 {
	if c == nil || c.Spec.Budgets == nil {
		return nil
	}
	return c.Spec.Budgets.MaxTokenBudget
}

// EffectiveDefaultTokenBudget is the number the shared rate-limit descriptor
// row is rendered with: the catalog's default if it has one, else the built-in
// constant, bounded either way by the catalog's maximum.
//
// This is the value every inheriting grant is actually enforced at, which is
// why it is also what the fallback in the policy's limit override uses. It runs
// through the same resolution function a grant does, so the row and a grant's
// reported budget cannot be computed two different ways.
func (c *AgentGatewayCatalog) EffectiveDefaultTokenBudget() int64 {
	return ResolveTokenBudget(nil, c.CatalogDefaultTokenBudget(), c.MaxTokenBudget()).Value
}
