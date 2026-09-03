# 3. Enforce per-session token budgets via the external rate-limit service

Date: 2026-09-03

## Status

Accepted

## Context

The failure mode worth guarding against in a workshop is an attendee's agent
entering a loop and exhausting the shared provider budget mid-session, taking
every other attendee down with it. Attribution alone does not prevent this; a
cumulative per-user cost cap is more than is needed for a two-hour workshop.
Blast-radius control is the target.

agentgateway offers three mechanisms:

**Per-key budgets** are the best fit conceptually — USD or token ceilings,
rolling windows, `onBudgetExceeded: Block`. But they require a database, which
reintroduces exactly what ADR-0001 removed. They also do not exist in the
Kubernetes-mode CRD: `budgets` and `allowedModels` appear only on the standalone
`LocalAPIKey` type.

**Local rate limiting** is an in-process token bucket with zero dependencies, but
counters are per-replica and per-gateway, not per-attendee. It protects the
provider budget in aggregate while doing nothing about one attendee starving the
rest.

**Global rate limiting** delegates to the Envoy rate-limit service over gRPC,
compatible with the reference `envoyproxy/ratelimit` implementation. Descriptors
are CEL expressions, so a per-attendee key is expressible.

Whether *token*-based limits work over the remote path was initially unclear.
They do, via a two-phase exchange (`http/remoteratelimit.rs:137-179,236-240`):
the request leg sends `hits_addend: 0`, and after the LLM responds an amend leg
rewrites it to the actual token count. `rls.proto:239-241` carries the per-
descriptor field, and the standard Envoy rate-limit server honours it.

Requests were considered as the unit and rejected: a single request with a large
context can cost more than a hundred small ones, so request counts are a poor
proxy for the thing being protected.

## Decision

Per-session token budgets, enforced by `envoyproxy/ratelimit` with Redis,
descriptors keyed on the participant key's session metadata.

## Consequences

Per-attendee limits, in the unit that actually tracks cost. One attendee's
runaway loop yields 429s for that attendee alone.

Two extra Deployments — the rate-limit service and Redis. Both are installed by
this project's Helm chart rather than reconciled by the operator, since they are
static infrastructure with no per-workshop variation. Redis needs **no
persistence**: counters are ephemeral, and losing them on restart resets budgets,
which is acceptable for ephemeral workshop clusters and keeps the "no long-term
persistence" constraint intact.

Two behaviours to design workshop content around, both documented upstream: rate
limiting runs *before* prompt guards, so a guardrail-rejected request still burns
budget; and the request that crosses the limit completes — only the next one is
rejected.

`failureMode` must be chosen deliberately. `FailClosed` is the default and means
a rate-limit service outage stops all LLM traffic; `FailOpen` means an outage
removes all protection. For a workshop, an outage that silently removes budget
enforcement is likely worse than one that visibly stops traffic.

Reversing this means either accepting per-gateway limits (local buckets) or
introducing the database that ADR-0001 rejected.
