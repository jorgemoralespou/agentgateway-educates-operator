# 04: Catalog-hosted default token budget

**What to build:** A cluster operator declares the ordinary token budget once,
on the **model catalog**, and every **session grant** that does not ask for
something specific gets it. A workshop author who wants the ordinary budget
writes nothing at all, and gets whatever the cluster operator decided — live,
rather than a value frozen into their workshop definition months ago.

Changing that default takes effect for running sessions that did not set their
own. This falls out of how the default reaches the Gateway rather than from a
watch: when a grant sets no budget, its **key registration** omits the budget
metadata entirely, and the Gateway falls through to the descriptor row in the
shared rate-limit service configuration. That row becomes catalog-driven. One
ConfigMap changes and every inheriting grant follows, with no fan-out watch
across grants and no registration rewritten.

Budgets go on the catalog rather than into a new custom resource kind. The
catalog is already cluster-scoped and operator-owned, already referenced by
every grant, already carries the rate-limit failure mode, and already has a
readiness gate grants wait on. This follows the reasoning already recorded on
that object's rate-limit field: it lives there because it is a choice about
serving LLM traffic, which is what the catalog governs.

The trade-off to accept: an inheriting grant's registration no longer shows its
effective budget. Offset by reporting the resolved value on the grant's status,
which is the object an operator reaches for first when asking why an attendee
got a 429.

Introduces the budget resolution chain as a pure function alongside the existing
accessors on the API types — grant value if set, else catalog default, else the
built-in constant.

**Blocked by:** 02, 03

**Status:** ready-for-agent

- [ ] A cluster operator can set a default token budget on the model catalog
- [ ] A grant that omits its budget is enforced at the catalog's default
- [ ] A grant that sets its budget is unaffected by the catalog's default
- [ ] Changing the catalog's default changes what an inheriting grant is
      enforced at, without any grant being reconciled or any registration
      rewritten
- [ ] A grant that omits its budget produces a registration carrying no budget
      metadata
- [ ] The shared rate-limit configuration's descriptor row reflects the
      catalog's default
- [ ] A catalog with no default falls back to the built-in constant
- [ ] The grant's status reports the effective token budget
- [ ] The resolution chain is a pure function with a table test covering set,
      unset-with-catalog-default, and unset-with-no-catalog-default
- [ ] Existing tests pass
