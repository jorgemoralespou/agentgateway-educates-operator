# 08: Record the decisions — ADR and glossary

**What to build:** The next person to read ADR-0003 should not have to
re-derive from rate-limit source code why it no longer says what shipped.

ADR-0003 rejected per-key budgets partly because "they require a database, which
reintroduces exactly what ADR-0001 removed", and noted that budgets "do not exist
in the Kubernetes-mode CRD". Both statements remain true of the mechanism it was
assessing: agentgateway's per-key budgets are standalone-only and SQLite-backed,
absent from the Kubernetes CRDs entirely. What changed is that cost budgets turn
out to be reachable **without** a database, through the global rate-limit path
this project already uses, because the descriptor's cost is a CEL expression with
access to the request's priced dollar cost. No SQLite, no database configuration,
no persistence added, and ADR-0001 stands untouched.

The same ADR should carry the second correction this work made: the enforcement
window was hourly rather than the session-lifetime cap the grant documented, and
no lifetime-scoped budget exists in this stack at all.

`CONTEXT.md` needs **cost budget** as a term — the per-session ceiling expressed
in US dollars — and its existing **token budget** entry revised. That entry's
closing clause, "measured in tokens rather than requests because cost tracks
tokens", was written when cost could not be measured directly. It can now.

Written last because it documents what actually shipped rather than what was
planned.

**Blocked by:** 07

**Status:** ready-for-agent

- [ ] An ADR records that cost budgets are reachable without a database via the
      global rate-limit path, amending or superseding the relevant part of
      ADR-0003
- [ ] That ADR states explicitly that ADR-0001's no-persistence constraint is
      untouched
- [ ] That ADR records the window correction: enforcement was hourly, never
      session-lifetime, and no lifetime-scoped budget exists in this stack
- [ ] `CONTEXT.md` defines **cost budget**
- [ ] `CONTEXT.md`'s **token budget** entry is revised now that cost is directly
      measurable
- [ ] The ADR follows the numbering and format of the existing ones
