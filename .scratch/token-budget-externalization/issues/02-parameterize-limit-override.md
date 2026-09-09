# 02: Prefactor — parameterize the policy's limit override

**What to build:** No user-visible behaviour change. This is a prefactor that
makes the three tickets after it small edits rather than three competing
rewrites of the same constant.

The rendered policy's limit override is currently a package-level CEL constant
with two values baked into its text: an hourly window and a literal fallback
budget. Three later tickets each need to vary one of those. Make the policy
renderer take the window unit and the fallback budget as arguments instead.

Take the opportunity to fix a real defect while the code is open: the fallback
in that expression is a hardcoded literal rather than the shared
`DefaultTokenBudget` constant, so the two can drift silently. Route it through
the constant. That leaves the schema marker as the only remaining duplicate of
the default, and ticket 04 removes that one.

Behaviour must be identical before and after: the same policy, byte for byte,
for the same inputs.

**Blocked by:** None (can start immediately)

**Status:** ready-for-agent

- [ ] The policy renderer accepts the window unit and the fallback budget as
      arguments rather than embedding them in a package-level constant
- [ ] The fallback budget in the rendered expression traces to the shared
      default constant, not a hardcoded literal
- [ ] The rendered policy is unchanged for the current inputs
- [ ] Existing tests pass without modification to their assertions
