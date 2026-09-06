# 05: Catalog-enforced maximum token budget

**What to build:** A cluster operator can bound what any workshop is allowed to
grant. This is the ticket that closes the trust boundary motivating the whole
feature: the person who owns the **provider credential** and pays for it decides
the ceiling, and a workshop author writing `session.objects` cannot exceed it.

A grant asking for more than the catalog's maximum is **clamped, not rejected**.
Rejection would fail every attendee's session at start; clamping means the
workshop still runs at a budget the operator is willing to pay for. The clamping
must be visible on the grant's status, so an author can tell their requested
value did not survive rather than spending a workshop debugging why attendees hit
a limit earlier than planned.

A catalog with no maximum clamps nothing, so this stays opt-in for operators who
do not need it.

Extends the resolution function introduced in 04 with the clamp, and its table
test with the cases that only exist once a maximum does.

**Blocked by:** 04

**Status:** ready-for-agent

- [ ] A cluster operator can set a maximum token budget on the model catalog
- [ ] A grant asking above the maximum is enforced at the maximum, and the
      grant still becomes Ready
- [ ] A grant asking at or below the maximum is enforced at its own value
- [ ] A catalog with no maximum clamps nothing
- [ ] The catalog's default is itself subject to the maximum, so an operator
      cannot configure a default that exceeds their own ceiling
- [ ] The grant's status reports both the effective budget and that clamping
      occurred
- [ ] The resolution function's table test covers above-maximum,
      at-maximum, below-maximum, and no-maximum-configured
- [ ] Existing tests pass
