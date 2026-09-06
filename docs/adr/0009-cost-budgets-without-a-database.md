# 9. Cost budgets are reachable without a database, and the window was never a session

Date: 2026-09-06

## Status

Accepted

Amends [ADR-0003](0003-token-budgets-via-external-ratelimit.md).

## Context

ADR-0003 chose the external rate-limit service and rejected agentgateway's
per-key budgets, partly because "they require a database, which reintroduces
exactly what ADR-0001 removed", and noted that budgets "do not exist in the
Kubernetes-mode CRD".

Both statements remain true of the mechanism that ADR was assessing.
agentgateway's per-key budgets are a standalone-mode feature backed by SQLite,
absent from the Kubernetes CRDs entirely. Nothing about that has changed, and
this ADR does not revisit it.

What was not appreciated at the time is that a **cost** ceiling does not need
that mechanism. A rate-limit descriptor's cost is a CEL expression, and the
expression has access to the realized dollar cost of the request, priced by
agentgateway against its own built-in model cost catalog: per-provider input,
output, cache and reasoning rates with context-length tiers. A budget in
dollars is therefore expressible over the same global rate-limit path this
project already uses, keyed on the same session metadata.

That matters because token budgets have a real weakness. Every model draws from
one bucket at one token per unit, so an attendee on an expensive model consumes
far more of the shared provider budget than one on a cheap model for the same
nominal ceiling. Blast-radius control was the stated goal of ADR-0003, and a
ceiling that means different things for different models controls it unevenly.

A second thing surfaced while implementing this. The grant's `tokenBudget`
documented itself as a ceiling "for this session's lifetime", but the rendered
policy used an hourly window. An attendee in a two-hour workshop received two
full budgets. The documentation described something the enforcement had never
implemented, and no lifetime-scoped budget exists anywhere in this stack: the
rate-limit service offers fixed windows aligned to the Unix epoch and nothing
else.

## Decision

Cost budgets in US dollars, enforced as a **second descriptor** beside the token
one on the same policy, sharing its window and session key, with the ceiling
carried per attendee on the key registration.

No database, no SQLite, no persistence, no configuration of either.
**ADR-0001's no-persistence constraint is untouched**, and so is ADR-0003's
rejection of per-key budgets, which remain database-backed and standalone-only.

The token descriptor stays, and stays enforced. It is not replaced. A cost
descriptor can be skipped silently: agentgateway drops a descriptor whose cost
expression fails to evaluate or does not yield a non-negative integer, logging
at debug level only. That is not covered by `failureMode`, which addresses the
rate-limit service being unreachable rather than a descriptor being dropped, and
there is no feedback path back to this operator. The token descriptor's cost
defaults to the total token count with no CEL involved, so it cannot be skipped,
which makes it the backstop when a cost expression fails.

For the same reason the cost expression tests for the presence of a priced cost
before using it and charges a pessimistic flat fallback when a model could not
be priced. agentgateway states plainly that a request is not charged when its
provider does not report the cost the budget unit needs, so unpriced models are
a real case. Charging nothing for them would turn an unpriced model into an
unmetered one, which is the failure the whole feature exists to prevent.

Dollars are scaled to **micro-dollars**, because the protocol field carrying a
descriptor's cost is an unsigned integer and the value must be whole. That is
also the grain agentgateway prices at internally. Whole cents would round most
individual requests to zero and enforce nothing. Authors write a decimal string,
parsed exactly: no floating-point field appears in any custom resource, since it
would admit representation errors into a value compared for equality.

Separately, the enforcement window becomes an explicit `budgetWindow` field on
the grant, defaulting to a day, and the `tokenBudget` documentation is corrected
to describe the window it actually gets rather than a session lifetime.

## Consequences

A ceiling that means the same thing regardless of which model an attendee picks,
in the unit the provider invoices in, without the database ADR-0001 rejected.

Two ceilings are now enforced per attendee, and whichever runs out first stops
them. An operator debugging a 429 has to know which one was hit; both are
reported on the grant's status for that reason.

This project still maintains no pricing data. It depends on agentgateway's
built-in cost catalog being present and current for the models a catalog names.
A model that catalog does not price is charged the flat fallback, which is
deliberately pessimistic and therefore wrong in the safe direction, but wrong.

Windows remain aligned to the Unix epoch rather than to a session's first
request. A daily window resets at midnight UTC, so a workshop spanning midnight
still yields two budgets. The guarantee is "at most one reset", not "no reset",
and the expiry sweep bounds the exposure because a session past its TTL cannot
spend the second budget. Removing that residual would need per-session windows,
which the rate-limit service does not offer.

Reversing this means dropping the cost descriptor and returning to token-only
budgets. Nothing else depends on it: the token descriptor is unchanged and
carries the enforcement on its own.
