# 01: Fix the stale registration write

**What to build:** A cluster operator or workshop author who edits a **session
grant**'s token budget sees the Gateway start enforcing the new ceiling. Today
they do not: the edit bumps the generation, the reconcile runs, the grant reports
`Ready` with an advanced `ObservedGeneration`, and the **key registration** keeps
the old value. The same staleness affects the grant's TTL, so `status.expiresAt`
reports one expiry while the registration carries another.

The cause is the guard that decides whether to rewrite a registration: it
compares only the key hash, so a reconcile renders the correct new payload and
then discards it. It must compare the whole rendered payload — equivalently, the
metadata map — so that a changed budget or expiry is written, while a genuinely
unchanged reconcile still performs no write at all.

The property that must survive is the one that actually matters and is currently
conflated with the defect: **a reconcile must never rotate a live attendee's
participant key.** Rotating mid-workshop signs an attendee out. The existing test
asserts both that and "an unchanged registration must not be rewritten" in one
block; the first is correct and must be kept, the second encodes the defect and
must be replaced by its inverse.

This is a pre-existing defect with no schema change, and it ships first and alone.
Everything downstream builds budget inheritance on top of this comparison —
building on it while it ignores the inherited value produces a feature that
appears to work and does not, and the test asserting the defect would otherwise
be read as a specification and preserved.

**Blocked by:** None (can start immediately)

**Status:** ready-for-agent

- [ ] Editing a grant's token budget causes the registration in the gateway
      namespace to carry the new value
- [ ] Editing a grant's TTL causes the registration's expiry to match what the
      grant's status reports
- [ ] A reconcile that changes nothing performs no write to the registration
- [ ] A reconcile never rotates a live attendee's participant key, including
      when the budget or TTL changed
- [ ] The existing test's two bundled assertions are split; the key-rotation
      assertion survives unchanged and the registration-rewrite assertion is
      inverted
- [ ] Existing tests pass
