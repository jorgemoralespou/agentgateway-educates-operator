# Third-party CRDs, for envtest only

The reconcilers create Gateway API and agentgateway resources, so envtest has to
serve those kinds. In production the Gateway API CRDs arrive with this
operator's own Helm chart (ADR-0006) and agentgateway's arrive from the charts
the platform installs; a test cannot wait for either, so the schemas are
vendored here.

Only the kinds this operator actually touches are kept. `AgentgatewayBackend`
and `HTTPRoute` are deliberately absent: the model-based flow does not need a
backend (credentials live on the concrete model), and no route is rendered.
Their schemas are large and would double the size of this directory for nothing.

## Provenance

`agentgateway/` — extracted from the `agentgateway-crds` 1.5.0 chart, the same
tarball embedded in `vendored-charts/`. They are plain YAML in that chart
despite living under `templates/`, so they are copied unmodified.

`gateway-api/` — from `sigs.k8s.io/gateway-api@v1.6.1`,
`config/crd/standard/`. Standard channel, matching what agentgateway 1.5.x
supports (Gateway API 1.4–1.6).

These are the same two files the chart ships in
`charts/agentgateway-educates-operator/templates/gateway-api-crds.yaml`, and
they track the `sigs.k8s.io/gateway-api` version in `go.mod` so the schemas the
tests serve match the types the operator compiles against. `make
vendor-gateway-api-crds` refreshes both places at once.

## Refreshing

These follow the pinned versions rather than the latest release. Refresh them
when `vendored-charts/` or the `sigs.k8s.io/gateway-api` dependency moves, and
not otherwise — a schema newer than the one the operator installs would let a
test pass against a field production does not have.
