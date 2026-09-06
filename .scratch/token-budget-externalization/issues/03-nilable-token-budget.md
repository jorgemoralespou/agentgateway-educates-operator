# 03: Make the grant's token budget a nil-able override

**What to build:** A workshop author can genuinely omit the token budget from a
**session grant**, and the operator can tell the difference between "the author
omitted it" and "the author asked for exactly the default".

Today it cannot. The schema stamps a default value into every grant that leaves
the field out, so by the time the controller reads the object the two cases are
indistinguishable. That is why no cluster-wide default can ever take effect, and
it is the groundwork every ticket after this one depends on.

The field becomes a pointer with no schema default and a minimum of one. Unset
resolves, through the existing accessor, to the built-in constant. This also
turns a defensive comment already in the code — about clients that strip zero
values — into a checked invariant: zero stops being a legal value, so a stripped
field is genuinely absent rather than silently registering a ceiling of no
tokens at all.

No behaviour changes for anyone. A grant that set the budget explicitly keeps
its value; a grant that relied on the stamped default now resolves to the same
constant by a different route. This is a v1alpha1 schema change requiring no
migration.

**Blocked by:** 01 — both touch the registration write path, and 01's corrected
test defines the baseline this change would otherwise be written against
wrongly.

**Status:** ready-for-agent

- [ ] A grant that omits the token budget is accepted and the field reads as
      unset rather than as a stamped default
- [ ] A grant that sets the token budget explicitly keeps that value
- [ ] A token budget of zero is rejected by validation
- [ ] An unset budget resolves to the built-in default constant, so the
      registration and the enforced ceiling are unchanged from today
- [ ] Generated CRD manifests and deepcopy are regenerated
- [ ] Existing tests pass, with fixtures updated for the new field shape
