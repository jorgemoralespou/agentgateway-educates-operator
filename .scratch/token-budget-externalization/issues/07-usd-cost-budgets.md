# 07: USD cost budgets

**What to build:** A budget can be expressed in the unit the provider actually
invoices in. A workshop author writes `costBudget: "0.50"` on a **session
grant**, or a cluster operator sets a default and maximum for it on the **model
catalog**, and an expensive **catalog model** consumes that budget faster than a
cheap one — so the ceiling means the same thing regardless of which model an
attendee picks. Today every model draws from the same bucket at one token per
unit.

This project maintains no pricing data. agentgateway prices each request against
its own built-in model cost catalog — per-provider input, output, cache and
reasoning rates with context-length tiers — and exposes the realized dollar cost
to CEL. That is what makes this feature possible without the database ADR-0001
rejected.

A cost budget is a **second descriptor** on the policy beside the token one,
keyed on the same session metadata and sharing the same window. It does not
replace the token descriptor. The token descriptor's cost defaults to the total
token count with no CEL involved, so it cannot be skipped, which makes it the
backstop when a cost expression fails — and cost expressions can fail silently.

Two properties the implementation must get right, both about that silent
failure. agentgateway skips a descriptor whose cost expression fails to evaluate
or does not yield a non-negative integer, logging at debug level only; this is
not covered by the rate-limit failure mode, which addresses the service being
unreachable rather than a descriptor being dropped, and there is no feedback path
back to this operator. So the expression must **test for the presence of cost
before using it and charge a pessimistic flat fallback** when a model could not
be priced — agentgateway states plainly that a request is not charged when its
provider does not report the cost the budget unit needs, so unpriced models are a
real case, not a hypothetical.

And the value must be an integer, because the protocol field carrying it is
unsigned integer. Dollars are scaled to **micro-dollars**, matching the grain
agentgateway uses internally. Truncation at that grain is negligible; a coarser
unit such as whole cents would round most individual requests to zero.

Authors write a decimal string, parsed exactly. No floating-point field appears
in any custom resource: it would admit representation errors into a value
compared for equality.

Introduces the dollar-string-to-micro-dollar conversion as a pure function.

**Blocked by:** 05, 06 — needs the catalog budget block and the shared window in
place.

**Status:** ready-for-agent

- [ ] A grant can declare a cost budget as a decimal dollar string
- [ ] A cluster operator can set a default and a maximum cost budget on the
      catalog, resolving and clamping as token budgets do
- [ ] A cost descriptor appears on the rendered policy only when a cost budget
      is configured
- [ ] The cost descriptor shares the token descriptor's window and session key
- [ ] The token descriptor remains present and enforced alongside it
- [ ] The cost expression tests for the presence of a priced cost and charges a
      pessimistic fallback when a model could not be priced
- [ ] Dollar values are converted exactly to micro-dollars with no
      floating-point field in any custom resource
- [ ] The conversion is a pure function with a table test covering ordinary
      values, sub-cent values, trailing zeros, excess decimal places, malformed,
      negative, and empty input
- [ ] The grant's status reports the effective cost budget
- [ ] Existing tests pass
