# 6. Ship the Gateway API CRDs from the operator's own chart

Date: 2026-09-03

## Status

Accepted

## Context

agentgateway is a Gateway API implementation, so the `gateway.networking.k8s.io`
CRDs have to exist before this operator can create a Gateway. They are distinct
from agentgateway's own CRDs, which arrive with the charts of ADR-0005.

The platform declaration has always offered `gatewayAPI: Managed` and
`gatewayAPI: Existing`, documented as "this operator applies the CRDs" and "they
are already present, leave them alone". But nothing applied them. `Managed` —
the default — checked whether the CRDs were present, found they were not, set
`GatewayAPIReady=False` with a message pointing at this operator's Helm chart,
and requeued. The chart did not install them either. On a cluster without
Gateway API the platform never became ready, and the loop had no exit.

The chart carried a `gatewayAPI.install` value documenting exactly the intended
behaviour, consumed by no template.

Three ways to close it.

**Apply them from the reconcile.** Symmetrical with how agentgateway's charts
are installed. But it would mean carrying a second copy of Gateway API in the
binary, and a reconcile that applies CRDs then immediately depends on them has
to handle its own REST mapper going stale mid-pass.

**Require them as a prerequisite.** Honest and simple, but it makes the default
configuration fail on a bare cluster, which is the case a workshop cluster is
most likely to be.

**Ship them with the chart.** They are a static, versioned artefact with no
per-cluster variation — exactly what a chart is for — and Helm applies them
before the Deployment, so the operator's mapper never sees a cluster without
them.

Where in the chart is not a free choice. Helm's `crds/` directory is
unconditional: it offers no way to honour a value, so `gatewayAPI.install:
false` could not work. A cluster whose ingress controller already owns Gateway
API must be able to opt out, and this operator must not fight it.

Version was also contested. The envtest fixtures were pinned at gateway-api
v1.4.0 while `go.mod` required v1.6.1 — a two-minor skew, benign only because
the operator touches nothing that changed.

## Decision

The chart ships the Gateway API CRDs from `templates/`, guarded by
`{{- if .Values.gatewayAPI.install }}` and annotated
`helm.sh/resource-policy: keep`. Only `GatewayClass` and `Gateway`, standard
channel, at the `sigs.k8s.io/gateway-api` version in `go.mod` — currently
v1.6.1. `make vendor-gateway-api-crds` regenerates the template and the envtest
fixtures together.

## Consequences

The default configuration now works on a bare cluster. `gatewayAPI: Managed`
with `gatewayAPI.install: true` installs the CRDs and the platform proceeds.

Opting out takes two settings that must agree: `gatewayAPI.install=false` on
the chart, and `spec.gatewayAPI: Existing` on the platform. Setting only the
first leaves `Managed` waiting for CRDs nobody installs — the same deadlock,
now reachable only by explicit misconfiguration. The deployment guide presents
both paths as equals rather than burying the second.

`resource-policy: keep` means `helm uninstall` leaves the CRDs behind. That is
deliberate: removing Gateway API from under an ingress controller that adopted
it in the meantime is a far worse failure than two CRDs outliving their chart.
It matches how the chart already treats its own CRDs, which Helm's `crds/`
never deletes either.

Unlike `crds/`, templated CRDs *are* upgraded by `helm upgrade`. For a schema
that tracks a pinned dependency this is what we want — the alternative is
CRDs that can only ever be updated by hand.

Shipping only two kinds means this chart does not provide `HTTPRoute`,
`GRPCRoute` or `ReferenceGrant`. Anything else on the cluster expecting a
complete Gateway API install must bring its own, and should own the CRDs
outright with `gatewayAPI.install=false`. Claiming API surface this operator
never reconciles would make a collision with a real ingress controller more
likely, not less.

The presence probe no longer trusts a cached miss: a no-match resets the REST
mapper and asks once more. Without that, a cluster operator who installs
Gateway API *after* this operator is running would leave the platform in
`Installing` until the pod restarted.

The version skew is gone — the schemas the tests serve and the types the
operator compiles against are now the same release, and one Make target keeps
them that way.

Reversing this means applying the CRDs from the reconcile instead, which brings
back the stale-mapper problem the chart placement avoids, or declaring Gateway
API a prerequisite and accepting that the default configuration fails on a bare
cluster.
