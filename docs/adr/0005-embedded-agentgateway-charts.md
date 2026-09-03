# 5. Install agentgateway from charts embedded in the operator image

Date: 2026-09-03

## Status

Accepted

## Context

This operator has to get agentgateway running on the cluster before it can hand
out participant keys. Three ways to do that were available.

**Require it as a prerequisite.** The cluster operator installs agentgateway
themselves and this operator only uses it. Simple, but it makes the common case
— a workshop cluster with nothing on it — a multi-step manual setup, and leaves
this operator unable to say anything useful when the version it finds is one it
has never been tested against.

**Fetch charts at reconcile time.** Pull from a registry when the platform is
declared. Always current, but it puts a network dependency on the reconcile
path, makes an air-gapped install impossible without a mirror, and means the
version installed depends on when reconcile happened to run.

**Embed the charts in the operator image.** The version is fixed at build time
and travels with the binary. This is the stance the Educates v4 installer
takes, which installs a hard-coded list of embedded charts and offers no
third-party extension point — the reason this operator is a peer of that
installer rather than a plugin to it.

The charts come from `ghcr.io/agentgateway/charts`. An earlier draft of this
decision named `cr.agentgateway.dev`; that registry denies anonymous pulls, and
the same charts are served from ghcr.io.

## Decision

The `agentgateway` and `agentgateway-crds` charts are vendored as tarballs next
to the code and embedded into the operator binary with `go:embed`. Their
versions are compile-time constants. Upgrading agentgateway means upgrading
this operator.

A cluster operator who needs a different version points this operator at a
gateway they installed themselves, with `provider: ExternalAgentgateway`. It
then installs nothing.

## Consequences

The coupling is real and is stated plainly rather than hidden: the operator is
tested against exactly the charts it ships. `make vendor-charts` refreshes them
deliberately, `SHA256SUMS` pins their integrity, and
`TestChartVersionsMatchTarballs` fails if a version constant and its tarball
disagree. `//go:embed` cannot escape its package directory, which is why the
tarballs live in `vendored-charts/` rather than somewhere tidier.

Installing charts that carry their own RBAC requires `bind` and `escalate` on
ClusterRoles, because Kubernetes forbids creating a role granting permissions
the creator lacks. That is approximately cluster-admin equivalent. Any operator
installing an RBAC-bearing chart needs it; this one says so in the chart README
rather than burying it, because it should be an informed decision. The
`ExternalAgentgateway` path needs no such permission.

Install order matters and is not obvious. The CRDs chart goes first: without
it the control plane starts and simply never becomes ready, which is a silent
failure rather than a loud one.

The operator waits for the GatewayClass that agentgateway's own controller
creates after leader election, and never creates it itself. A ready Deployment
is therefore not sufficient to proceed on.

Readiness gates on the Gateway reporting `Programmed` and its data-plane
Deployment being available — never on the Gateway's addresses. A workshop
cluster with no load balancer provider produces a Gateway that is working and
addressless, and gating on addresses would call that broken.

Configuration is by typed fields and parameter overlays, not value passthrough.
The chart's defaults are already right for a workshop cluster, and a knob whose
default is correct everywhere is surface without a decision behind it. The
data-plane Service type is the exception, and it is not a chart value at all:
it is set through an `AgentgatewayParameters` overlay on the GatewayClass,
because the data plane is rendered per Gateway by agentgateway's controller
rather than by the chart.

Releases carry an ownership label. The v4 installer does not do this, and
without it two operators sharing a release name fight in an upgrade loop, each
reverting the other.

Teardown drains rather than orphans. Switching the provider to external
uninstalls the bundled release instead of leaving it running — the bug in v4's
cluster-services path that its platform-extras path does not have. Draining
tolerates kinds disappearing underneath it, because `EducatesClusterConfig`'s
teardown guard hardcodes the three platform component kinds and will not wait
for this operator's resources: cluster services, and the CRDs defining those
kinds, can be removed while this operator is still draining. The namespace goes
last, and only because this operator created it.

Reversing this means moving chart resolution to reconcile time and accepting a
network dependency there, or dropping to `ExternalAgentgateway` everywhere and
requiring cluster operators to install agentgateway themselves.

## Notes

Chart provenance, including the digests of the 1.5.0 tarballs and the
`cr.agentgateway.dev` correction, is recorded in
`.scratch/agentgateway-operator/crd-shapes.md`.

The Gateway API CRDs are a separate question from agentgateway's own, and are
decided in ADR-0006.
