# 06: Explicit budget window, decoupled from TTL

**What to build:** An attendee's budget lasts their whole session instead of
silently resetting partway through, and the **session grant** says what it
actually enforces.

Today the grant documents its token budget as a ceiling "for this session's
lifetime" while the rendered policy uses an hourly window, so an attendee in a
two-hour workshop receives two full budgets. The documentation promises
something the enforcement has never implemented.

A new `budgetWindow` field on the grant says how long a budget lasts, defaulting
to a day, which covers any workshop. It flows into the policy's limit override in
place of the hardcoded hour. Legal values are the units agentgateway accepts:
second, minute, hour, day, month, year.

`ttl` is deliberately left alone. It keeps its free-form duration pattern and its
existing default, and it remains the expiry backstop — per ADR-0002 the only
protection when a force-deleted namespace orphans a registration. Restricting it
to the window enum was considered and rejected: it would make the current default
illegal, break every existing grant, and trade the expiry guarantee for the rate
limiter's vocabulary. The two fields stop being entangled.

One residual is accepted rather than engineered around, and must be documented:
windows are aligned to the Unix epoch, not to a session's first request. A daily
window resets at midnight UTC, so a workshop spanning midnight yields two
budgets. The guarantee is "at most one reset", not "no reset", and the expiry
controller bounds the exposure because a session past its TTL cannot spend the
second budget. No lifetime-scoped budget exists anywhere in this stack.

The grant's token budget doc-comment must be corrected to describe the window it
actually gets.

**Blocked by:** 02, 03

**Status:** ready-for-agent

- [ ] A grant can declare its budget window, and defaults to a day when it does
      not
- [ ] The declared window reaches the rendered policy's limit override in place
      of the hardcoded hour
- [ ] A grant's TTL keeps its existing pattern and default, and existing grants
      using it remain valid
- [ ] Changing the budget window does not change the expiry, and vice versa
- [ ] The token budget's documentation describes the real window rather than
      claiming a session lifetime
- [ ] The epoch-alignment residual is documented where an operator will find it
- [ ] Generated CRD manifests are regenerated
- [ ] Existing tests pass
