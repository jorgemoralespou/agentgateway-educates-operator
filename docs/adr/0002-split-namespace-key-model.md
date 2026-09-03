# 2. Accept a split-namespace key model and clean up with a bounded finalizer

Date: 2026-09-03

## Status

Accepted

## Context

An attendee's key must exist in two forms, and two Kubernetes namespaces will not
let them live together.

The **participant key**'s plaintext must be in a Secret in the *workshop*
namespace. This is not a preference. Educates creates each attendee's dashboard
Deployment in the workshop namespace (`session-manager/handlers/workshopsession.py:1556`,
verified on `develop` at `d7bf2058`), while `session.objects` default to the
*session* namespace (`:1262`). A pod can only resolve a `secretKeyRef` within its
own namespace, so a Secret left in the session namespace is unreachable and the
pod wedges in `CreateContainerConfigError` indefinitely. The custom resource must
therefore set `namespace: $(workshop_namespace)` explicitly.

The **key registration** must be in the *gateway* namespace. agentgateway's
selector is pinned to the policy's own namespace in compiled code:

    krt.FilterIndex(ctx.Collections.ConfigMapsByNamespace, policy.Namespace)

There is no `namespaceSelector` field, and `ReferenceGrant` — which agentgateway
supports for backend references — is never consulted in the API-key path.

Two workarounds were considered and rejected on evidence:

*One policy per workshop namespace.* Not expressible: `targetRefs` has no
namespace field, so a policy cannot target a Gateway elsewhere. And API-key
policies **replace** rather than merge (`store/policy.rs:266-284`,
`*self = policy.clone()`), unlike authorization policies which accumulate. Two
policies on one Gateway means one silently wins by creation order, with no error
surfaced. Attendees would lose access non-deterministically.

*A SecretCopier hop.* The Educates charts use SecretCopier for cross-namespace
Secrets, so it is house-idiomatic. Rejected because it adds a reconcile hop on
the session-start critical path, where latency directly delays every attendee's
container start.

Educates owns session objects by the session *namespace*, not the WorkshopSession
CR (`kopf.adopt(object_body, namespace_instance.obj)`, `:1283` — the published
docs say otherwise and are stale). So the Secret is inside the blast radius of
session teardown; the registration, in a namespace that outlives every session,
is not.

## Decision

Accept the split. The operator writes the plaintext Secret to the workshop
namespace and the hash-only registration to the gateway namespace, and removes
the registration with a **finalizer bounded by a timeout**: retry for a fixed
window, then log loudly, release the finalizer, and let deletion proceed.
Independently, every key carries a TTL so it expires without any cleanup running
at all.

## Consequences

Session teardown removes both objects in the normal case.

A finalizer that cannot complete blocks the entire session namespace in
`Terminating`. Educates makes this deliberately visible — that is why the
namespace owns session objects. Bounding the finalizer trades a rare leaked
registration for never wedging a cluster, which is the right way round: leaked
registrations are recoverable by an out-of-band sweep, wedged namespaces need
operator intervention per session.

When cleanup does fail, what leaks is a SHA-256 hash, not a credential. The
plaintext died with the workshop namespace. An orphaned registration is a stale
record that opens nothing for anyone not already holding the key, and the TTL
closes even that window. This asymmetry — hash outlives plaintext, never the
reverse — is what makes the whole arrangement tolerable.

Force-deleting a namespace (`--grace-period=0`) strips finalizers and orphans the
registration outright. The TTL is the only protection in that case, which is why
it is not optional.

## Notes

The workshop-namespace behaviour is read from source, not observed end-to-end. It
must be validated against a live Educates v4 cluster — covering teardown as well
as startup — before this design is built on.
