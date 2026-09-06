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
}

// ResolveTokenBudget decides the ceiling one grant is enforced at.
//
// Precedence: the grant's own value if it set one, else the catalog's default,
// else the built-in constant. Both arguments are nil-able because "unset" is a
// meaningful state at every link: it is what lets an omitted budget inherit
// rather than freeze whatever the default happened to be when the grant was
// written.
func ResolveTokenBudget(grant *int64, catalogDefault *int64) ResolvedTokenBudget {
	if grant != nil && *grant > 0 {
		return ResolvedTokenBudget{Value: *grant}
	}

	// Inherited either way: the grant asked for nothing, so the registration
	// carries no budget and the shared descriptor row decides. Whether that row
	// came from the catalog or from the built-in constant is invisible to the
	// grant, which is what makes a later catalog edit take effect.
	if catalogDefault != nil && *catalogDefault > 0 {
		return ResolvedTokenBudget{Value: *catalogDefault, Inherited: true}
	}
	return ResolvedTokenBudget{Value: DefaultTokenBudget, Inherited: true}
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

// EffectiveDefaultTokenBudget is the number the shared rate-limit descriptor
// row is rendered with: the catalog's default if it has one, else the built-in
// constant.
//
// This is the value every inheriting grant is actually enforced at, which is
// why it is also what the fallback in the policy's limit override uses.
func (c *AgentGatewayCatalog) EffectiveDefaultTokenBudget() int64 {
	if d := c.CatalogDefaultTokenBudget(); d != nil && *d > 0 {
		return *d
	}
	return DefaultTokenBudget
}
