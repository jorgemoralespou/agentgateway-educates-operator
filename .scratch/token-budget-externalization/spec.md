# Externalize token budgets and add cost budgets

Status: ready-for-agent

## Problem Statement

A **session grant** carries its own `tokenBudget`, and that is the only place a
**token budget** can be expressed. Three problems follow.

**The cluster operator has no ceiling.** A workshop author writes
`session.objects` and picks whatever budget they like. Nothing clamps it. The
person who owns the **provider credential** and pays for it cannot bound what a
workshop grants, even though guarding that credential is the entire reason the
budget exists. This is a trust boundary — the grant is written by the
less-trusted party — and the current schema has no way to express it.

**A default that cannot be defaulted.** `tokenBudget` carries
`+kubebuilder:default=100000`, so the API server stamps `100000` into every
grant that omits it. By the time the controller reads the object, "the author
omitted it" and "the author explicitly asked for 100000" are indistinguishable.
Any cluster-wide default is therefore unreachable: there is no such thing as an
unset budget to fall back from.

**Budget edits silently do nothing.** Editing `spec.tokenBudget` on a live grant
bumps the generation, runs a reconcile, advances `ObservedGeneration` and sets
`Ready` — while the **key registration** keeps the old value and the Gateway
keeps enforcing the old ceiling. The reconcile renders the correct new payload
and then discards it, because the write is guarded by a comparison of the key
hash alone. The same defect staleness affects `expiresAt`: `status.expiresAt` reports the
new expiry while the registration carries the old one, so status and enforcement
disagree. The existing test asserts this behaviour as correct.

Separately, the budget's stated semantics and its enforcement do not match. The
grant documents `tokenBudget` as "the ceiling on LLM tokens for this session's
lifetime". The rendered policy uses an hourly window, so an attendee in a
two-hour workshop receives two full budgets. And the default value now exists in
three places that can drift independently: the kubebuilder marker, the
`DefaultTokenBudget` constant, and a hardcoded literal in the policy's CEL
fallback.

Finally, a token budget is a proxy for the thing actually being protected, which
is spend. Every **catalog model** draws from the same bucket at one token per
unit, so an expensive model and a cheap one consume an attendee's budget
identically. A workshop author who wants to say "each attendee gets fifty cents"
cannot.

## Solution

Budgets become a property of the **model catalog** — which already governs how
LLM traffic is served, and already carries the rate-limit failure mode — with
the **session grant** able to override within limits the catalog sets.

A cluster operator declares default and maximum budgets once on the catalog. A
workshop author may set a budget on a grant; if they set none, the catalog's
default applies; if they set one above the catalog's maximum, it is clamped and
the clamping is visible in the grant's status. An author who wants the ordinary
budget writes nothing at all, and gets whatever the cluster operator decided,
live — not a value frozen into their workshop definition months ago.

Budgets can be expressed in **US dollars** as well as tokens. agentgateway
prices each request against its own built-in model cost catalog, so a cost
budget needs no pricing data maintained by this project. An author writes
`costBudget: "0.50"`; expensive and cheap models then consume that budget at
their true relative rates. The token budget stays and stays enforced: it is the
backstop that survives a cost expression failing.

The budget window becomes explicit rather than implied. A new `budgetWindow`
field says how long a budget lasts, defaulting to a day, which covers any
workshop. `ttl` keeps its existing meaning and default as the expiry backstop
and is no longer entangled with enforcement.

And a budget edit takes effect.

## User Stories

1. As a cluster operator, I want to set a default token budget on the model
   catalog, so that every session grant that does not ask for something specific
   gets the ceiling I chose.
2. As a cluster operator, I want to set a maximum token budget on the model
   catalog, so that a workshop author cannot grant more of my provider
   credential than I am willing to spend.
3. As a cluster operator, I want a grant asking for more than the maximum to be
   clamped rather than rejected, so that a workshop still runs instead of every
   attendee failing to start.
4. As a cluster operator, I want to change the default budget and have running
   sessions that did not set one pick it up, so that I can respond to a
   provider bill without editing anyone's workshop.
5. As a cluster operator, I want the budget defaults to live next to the model
   catalog and the failure mode, so that every decision about serving LLM
   traffic is in one object.
6. As a cluster operator, I want to express budgets in US dollars, so that the
   ceiling I set matches the unit my provider invoices me in.
7. As a cluster operator, I want an expensive model to consume more of an
   attendee's budget than a cheap one, so that the ceiling means the same thing
   regardless of which catalog model an attendee picks.
8. As a cluster operator, I want a token budget enforced even when I have
   configured a cost budget, so that a failure in cost accounting cannot leave
   an attendee unbounded.
9. As a cluster operator, I want the budget window to be at least as long as a
   workshop, so that an attendee cannot receive a second full budget partway
   through.
10. As a cluster operator, I want to keep the existing expiry backstop unchanged
    when I change the budget window, so that tightening one does not loosen the
    other.
11. As a workshop author, I want to omit the budget from a session grant
    entirely, so that my workshop inherits whatever the cluster considers
    normal.
12. As a workshop author, I want to set a higher budget on a grant for a
    workshop that genuinely needs one, so that a long agentic exercise is not
    capped at the ordinary value.
13. As a workshop author, I want to write a cost budget as an ordinary dollar
    amount like `"0.50"`, so that I do not have to convert to tokens or to some
    internal unit.
14. As a workshop author, I want my existing session grants to keep working
    unchanged after this feature ships, so that I do not have to revisit every
    workshop.
15. As a workshop author, I want `ttl: 4h` to remain valid, so that my current
    workshop definitions are not rejected.
16. As a workshop author, I want to see the budget actually in force on the
    grant's status, so that I can tell whether my requested value survived the
    catalog's maximum.
17. As a workshop author, I want to know when my requested budget was clamped,
    so that I do not spend a workshop debugging why attendees hit a limit
    earlier than I planned.
18. As a workshop author, I want to edit a grant's budget mid-cohort and have it
    take effect, so that I can react to attendees running out.
19. As a workshop author, I want the grant's documented budget semantics to
    match what the Gateway enforces, so that I can explain the limit to
    attendees truthfully.
20. As an attendee, I want a budget that lasts my whole session, so that my
    limit does not silently reset and mislead me about what I have left.
21. As an attendee, I want a runaway agent to exhaust only my own budget, so
    that I do not take the rest of the workshop down with me.
22. As an attendee, I want to keep working when another attendee hits their
    limit, so that one person's loop does not become everyone's outage.
23. As an attendee, I want my participant key to survive a budget change, so
    that a cluster operator adjusting a default does not sign me out mid-exercise.
24. As an operator debugging a support question, I want to see the effective
    budget on the grant's status without reading the gateway namespace, so that
    I can answer "why did this attendee get a 429" from one object.
25. As an operator debugging a support question, I want a grant whose budget
    could not be resolved to say so in its conditions, so that a misconfigured
    catalog is visible rather than silent.
26. As a maintainer, I want the default budget to exist in exactly one place, so
    that the schema default, the Go constant and the policy's CEL fallback
    cannot drift apart.
27. As a maintainer, I want an unset budget to be distinguishable from a budget
    of zero, so that a client that strips zero values cannot accidentally
    register a ceiling of no tokens at all.
28. As a maintainer, I want a registration that omits the budget to fall back to
    a catalog-driven value, so that adding the default does not require watching
    every grant in the cluster.
29. As a maintainer, I want a cost budget whose pricing data comes from
    agentgateway, so that this project never maintains a table of per-model
    prices that will go stale.
30. As a maintainer, I want a request against an unpriced model to be charged
    something rather than nothing, so that a gap in the pricing catalog does not
    become a hole in enforcement.
31. As a maintainer, I want the dollar-to-integer conversion to be exact, so
    that floating-point representation cannot make two equal budgets compare
    unequal.
32. As a maintainer, I want the reasoning behind reopening the cost-budget
    decision recorded in an ADR, so that the next person does not re-derive it
    from the rate-limit source.

## Implementation Decisions

### No new custom resource kind

Budgets go on the existing **model catalog**, not into a new kind. The catalog
is already cluster-scoped and operator-owned, already referenced by every
session grant through `catalogRef`, already carries `rateLimit.failureMode`, and
already has a readiness gate the grant waits on. A separate kind would duplicate
that reference plumbing and add a second object that can be missing, unready or
dangling.

This follows the reasoning already recorded on the catalog's `rateLimit` field:
it lives there "because it is a choice about serving LLM traffic, which is what
the catalog governs, and the platform's job is installing the gateway rather
than deciding how it behaves under load". A token budget is exactly such a
choice.

### Catalog schema

The catalog's existing rate-limit block gains budget settings: a default token
budget, a maximum token budget, and their cost equivalents. All optional; an
absent default falls back to the existing built-in constant, and an absent
maximum means no clamping.

### The grant's budget becomes optional in the schema sense

`tokenBudget` changes from `int64` with a schema default to a **pointer with no
schema default** and a minimum of 1. This is what makes "unset" representable.
Without it the API server stamps a value into every grant and a catalog default
can never fire.

This also turns the existing defensive comment about clients that strip zero
values into a checked invariant: zero is no longer a legal value, so a stripped
field is `nil` and resolves through the chain rather than registering a ceiling
of no tokens.

This is a v1alpha1 schema change. Existing grants that set the field explicitly
are unaffected; grants that relied on the stamped default now resolve through
the catalog to the same built-in constant, so behaviour is preserved.

### Budget resolution

A pure resolution function on the API types, alongside the existing
`TokenBudget()` and `FailureMode()` helpers: grant value if set, else catalog
default, else built-in constant — then clamped to the catalog maximum if one is
set. The resolved value and whether clamping occurred are both reported on the
grant's status.

### Defaults reach the Gateway through the shared rate-limit configuration, not through each registration

When a grant sets no budget, the **key registration** omits the budget metadata
entirely rather than carrying a resolved value. The Gateway then falls through
to the descriptor row in the shared rate-limit service configuration, which the
platform controller already writes and which is already deliberately set equal
to the default rather than to something permissive.

That row becomes catalog-driven. The consequence is that changing the catalog
default takes effect for every grant that did not set one, without watching
grants and without re-writing any registration — one ConfigMap changes. It also
collapses the three-way default drift, because the policy's CEL fallback and the
shared configuration row both trace to the same constant.

The cost is that the effective budget for an inheriting grant is not visible in
its registration. This is offset by reporting the resolved value on the grant's
status, which is the object an operator would reach for first.

### The registration write must compare the whole payload

The guard that decides whether to rewrite a registration currently compares the
key hash alone, which is why budget and TTL edits never reach the Gateway. It
must compare the full rendered payload, or equivalently the metadata map, so
that a changed budget or expiry is written while an unchanged reconcile still
performs no write.

The property that must survive is the one that matters: a reconcile must never
rotate a live attendee's **participant key**. That is separate from, and
currently conflated with, "an unchanged registration must not be rewritten".

This is a pre-existing defect independent of the rest of this work, and ships
first, on its own.

### The budget window becomes an explicit field, decoupled from TTL

A new `budgetWindow` enum on the grant, defaulting to `day`. Legal values are
the units agentgateway accepts in a limit override: second, minute, hour, day,
month, year.

`ttl` keeps its free-form duration pattern and its `4h` default. It remains the
expiry backstop — per ADR-0002 the only protection when a force-deleted
namespace orphans a registration — and no longer participates in enforcement.
Restricting `ttl` to the enum was considered and rejected: it would have made
the existing `4h` default illegal, breaking every current grant, and would have
traded the expiry guarantee for the rate limiter's vocabulary.

The window flows into the policy's limit override, replacing the hardcoded
hourly unit.

Note the residual, which is accepted rather than engineered around: windows are
aligned to the Unix epoch, not to a session's first request. A `day` window
resets at midnight UTC, so a workshop spanning midnight yields two budgets. The
guarantee is "at most one reset", not "no reset", and the expiry controller
bounds the exposure because a session past its TTL cannot spend the second
budget. No lifetime-scoped budget exists anywhere in this stack; the grant's
doc-comment claiming one must be corrected.

### Cost budgets ride the existing global rate-limit path

A cost budget is a **second descriptor** alongside the token descriptor on the
same policy, keyed on the same session metadata, sharing the same window. Not a
replacement: the token descriptor's cost defaults to the total token count with
no CEL involved, so it cannot be skipped, which makes it the backstop when a
cost expression fails.

Two windows were rejected: an attendee blocked on tokens but clear on cost at a
different moment is not explainable in workshop content.

A catalog-wide spend ceiling — as opposed to per-attendee — is out of scope. The
descriptor is keyed on the session, so a global ceiling would need a third
descriptor with a constant key. Noted as future work.

### Cost is carried in micro-dollars

agentgateway exposes the realized dollar cost of a request to CEL, priced from
its own built-in model cost catalog with per-provider input, output, cache and
reasoning rates and context-length tiers. This project maintains no pricing
data.

The value must be an integer, because the rate-limit protocol field carrying it
is unsigned integer. Dollars are therefore scaled to **micro-dollars** in the
descriptor's cost expression, and budgets are scaled the same way, matching the
grain agentgateway itself uses internally. Truncation at micro-dollar grain is
negligible; a coarser unit such as whole cents would round most individual
requests to zero.

### The cost expression is guarded

agentgateway skips a descriptor whose cost expression fails to evaluate or does
not yield a non-negative integer, logging at debug level only. This is not
covered by `failureMode`, which addresses the rate-limit service being
unreachable, not a descriptor being dropped. There is no feedback path from a
skipped descriptor back to this operator, so it cannot be surfaced in status.

Two mitigations, both required. The expression tests for the presence of cost
before using it and charges a **pessimistic flat fallback** when a model could
not be priced — agentgateway states plainly that a request is not charged when
its provider does not report the cost the budget unit needs, so unpriced models
are a real case. And the token descriptor remains as the enforcement that cannot
silently stop.

### Cost budgets are authored as decimal strings

The author writes a dollar amount as a string, validated by pattern, and the
operator parses it exactly and scales to micro-dollars. No floating-point field
appears in any custom resource: `float64` would admit representation errors into
a value that gets compared for equality.

### Contradicts ADR-0003, deliberately

ADR-0003 rejected per-key budgets partly because "they require a database, which
reintroduces exactly what ADR-0001 removed", and noted that budgets "do not
exist in the Kubernetes-mode CRD". Both statements remain true of the mechanism
it was assessing — agentgateway's per-key budgets are standalone-only and
SQLite-backed, absent from the Kubernetes CRDs entirely.

What has changed is that cost budgets turn out to be reachable **without** a
database, through the global rate-limit path this project already uses, because
the descriptor's cost is a CEL expression with access to the request's priced
dollar cost. No SQLite, no `config.database`, no persistence added, and ADR-0001
stands untouched.

This reopening must be recorded as an ADR amending or superseding the relevant
part of ADR-0003, alongside the correction that the enforcement window was
hourly rather than lifetime-scoped.

### Glossary

**Token budget** is defined in `CONTEXT.md` as the per-session ceiling on LLM
tokens. A **cost budget** is a new term for the same ceiling expressed in US
dollars, and needs adding. The existing definition's closing clause — "measured
in tokens rather than requests because cost tracks tokens" — should be revisited
now that cost is measurable directly.

## Testing Decisions

A good test here asserts external behaviour: the shape of what is handed to
agentgateway, and what an operator or author observes on a custom resource. It
does not assert that a particular helper was called or that an intermediate
struct has a given field. The rendered policy and the rendered registration are
the real contracts with the Gateway, and both are already produced by pure
functions specifically so they can be asserted without a cluster.

Prefer existing seams. Four of the six below already exist.

### Existing seams

**Policy rendering.** `renderPolicySpec` is a pure function and is already the
seam used to assert policy shape without an API server; its file comment records
that this is deliberate, because the policy is "easy to get subtly wrong and
expensive to debug on a cluster". Assert here: the limit override carries the
grant's window rather than a hardcoded hour; the token descriptor is unchanged
in shape; the cost descriptor appears only when a cost budget is configured; the
cost expression is guarded and scales to micro-dollars; the fallback in the
override traces to the shared constant rather than a literal. Prior art: the
existing request-timeout and failure-mode assertions in the same file, including
the pattern of asserting that an unset value is absent entirely rather than
written as a default.

**Registration rendering.** `renderRegistration` is pure and already covered.
Assert here: budget metadata is omitted entirely when the grant sets no budget,
so the Gateway falls through to the shared configuration row; it is present and
correct when the grant sets one.

**Session controller, envtest.** The stale-registration fix is about reconcile
behaviour across an update, not about rendering, so it needs a real API server.
The existing test that bundles "must never rotate a live attendee's key" with
"an unchanged registration must not be rewritten" must be **split**: the first
assertion is correct and must survive; the second encodes the defect and must be
replaced by its inverse — a changed budget rewrites the registration and the new
value is observable in the gateway namespace, while a genuinely unchanged
reconcile still performs no write. Add: the expiry in the registration tracks a
changed TTL, so status and enforcement agree. Prior art: the self-healing
describe block in the same file, which already drives a full reconcile and
inspects the gateway-namespace ConfigMap.

**Platform controller, envtest.** Assert that the shared rate-limit
configuration's descriptor row reflects the catalog's default budget, and that
changing the catalog default updates it. Prior art: the existing platform
controller tests covering the rate-limit ConfigMap and policy convergence.

### New seams

Two, both pure functions on the API types, both mirroring helpers that already
exist there (`TokenBudget()`, `FailureMode()`, `CatalogName()`). No new envtest
suite, no mocks.

**Budget resolution.** Table test over the chain: grant set, grant unset with a
catalog default, grant unset with no catalog default, grant above the catalog
maximum, catalog maximum absent. Assert both the resolved value and the clamping
signal.

**Dollar parsing.** Table test over the string-to-micro-dollar conversion:
ordinary values, sub-cent values, trailing zeros, more decimal places than
micro-dollars can represent, malformed input, negative input, empty input. This
is where precision bugs would hide, and it deserves direct assertions rather
than being inferred from rendered YAML.

### Not tested here

That agentgateway prices a given model correctly, that the rate-limit service
counts correctly, and that epoch-aligned windows reset when they do. These are
upstream behaviours; asserting them would be testing dependencies. The e2e suite
already exercises the live enforcement path end to end.

## Out of Scope

- **A catalog-wide or workshop-wide spend ceiling.** Descriptors are keyed on
  the session, so an aggregate ceiling needs a third descriptor with a constant
  key. Worth doing, separate feature.
- **Per-model budgets or allowlists.** agentgateway's `allowedModels` is
  standalone-only, like `budgets`.
- **Refunding an over-charge.** agentgateway skips negative amendments, so this
  is not available regardless of what the rate-limit protocol supports.
- **Surfacing a skipped cost descriptor.** There is no feedback path from the
  data plane back to this operator. Mitigated by the guard and the token
  backstop, not solved.
- **Eliminating the epoch-aligned window reset.** No lifetime-scoped budget
  exists in this stack. Documented and bounded by the expiry controller.
- **Watching the registration ConfigMap for external edits.** A registration
  manually edited or deleted in the gateway namespace is still not repaired
  except through a Secret change or a generation bump. Pre-existing, noted while
  tracing this work, not fixed here.
- **Adopting agentgateway's standalone per-key budgets.** They require
  `config.database` and SQLite, which ADR-0001 rejected and this work does not
  reopen.
- **Migrating existing grants.** The schema change is backward-compatible; no
  data migration is required.

## Further Notes

**Ordering matters.** The stale-registration fix ships first and alone. Building
budget inheritance on top of a comparison that ignores the inherited value
produces a feature that appears to work and does not — and the test currently
asserting the defect would otherwise be read as a specification and preserved.

After that: the pointer change and resolution chain; the catalog budget block
and the catalog-driven fallback row; the window field; the cost descriptor. The
ADR and the `CONTEXT.md` glossary entry land with the cost work, since that is
what reopens ADR-0003.

**Verification note.** The upstream facts this spec depends on — that cost is
exposed to CEL from a built-in pricing catalog, that the cost field is an
integer, that a failed expression skips the descriptor silently, that windows
are epoch-aligned, and that `budgets` is absent from the Kubernetes CRDs — were
read from agentgateway and envoyproxy/ratelimit sources at the time of writing.
ADR-0001 records the practice of verifying against a pinned agentgateway
revision; the implementer should confirm against the version this project
vendors before relying on the cost path's exact semantics.

**One documentation defect is fixed in passing.** The grant's `tokenBudget`
doc-comment describes a ceiling "for this session's lifetime", which the
enforcement has never implemented. It must say what the window actually is.
