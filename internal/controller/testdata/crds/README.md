# Third-party CRDs, for envtest only

The reconcilers create Gateway API and agentgateway resources, so envtest has to
serve those kinds. In production this operator installs them itself; a test
cannot wait for that, so the schemas are vendored here.

Only the kinds this operator actually touches are kept. `AgentgatewayBackend`
and `HTTPRoute` are deliberately absent: the model-based flow does not need a
backend (credentials live on the concrete model), and no route is rendered.
Their schemas are large and would double the size of this directory for nothing.

## Provenance

`agentgateway/` — extracted from the `agentgateway-crds` 1.5.0 chart, the same
tarball embedded in `vendored-charts/`. They are plain YAML in that chart
despite living under `templates/`, so they are copied unmodified.

`gateway-api/` — from `sigs.k8s.io/gateway-api@v1.4.0`,
`config/crd/standard/`. Standard channel, matching what agentgateway 1.5.x
supports (Gateway API 1.4–1.6).

## Refreshing

These follow the pinned versions rather than the latest release. Refresh them
when `vendored-charts/` or the `sigs.k8s.io/gateway-api` dependency moves, and
not otherwise — a schema newer than the one the operator installs would let a
test pass against a field production does not have.
