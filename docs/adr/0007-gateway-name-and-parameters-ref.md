# 7. Name the Gateway apart from the control plane, and reference the overlay from it

Date: 2026-09-04

## Status

Accepted

## Context

Two defects, found together on a live Educates cluster, both rooted in the same
misreading of how agentgateway turns a Gateway into running pods.

agentgateway provisions the data plane by generating a Deployment, Service and
ServiceAccount **named after the Gateway, verbatim, in the Gateway's own
namespace**. This was confirmed empirically: a Gateway called `probe-dataplane`
produced a Deployment, Service and ServiceAccount all called
`probe-dataplane`.

### The name collision

This operator installs the agentgateway control plane as a Helm release called
`agentgateway` into `agentgateway-system` (ADR-0005), and then created a Gateway
also called `agentgateway` in that same namespace. The generated data-plane
Deployment therefore targeted the name the control-plane Deployment already
held.

The two are not interchangeable. The control plane runs
`cr.agentgateway.dev/controller` and carries the selector Helm gave it; the data
plane wants a selector containing `gateway.networking.k8s.io/gateway-name`. A
Deployment's `spec.selector` is immutable, so the apply is rejected by the API
server, not once, but on every retry:

```
failed to apply object apps/v1, Kind=Deployment agentgateway-system/agentgateway:
Deployment.apps "agentgateway" is invalid: spec.selector: Invalid value: {...}:
field is immutable
```

The Gateway reached `Accepted=True` and stuck at `Programmed=False` with reason
`DeploymentFailed`, retrying forever. The platform never became ready. Nothing
about the error names the real cause, and the control-plane Deployment sits
there at `1/1 Available`, which makes the namespace look healthy.

Sessions still worked in casual testing, which is why this survived review: the
control-plane Service happens to expose port 4000, so the URL handed to sessions
resolved to something listening. It resolved to the wrong process.

### The inert overlay

Separately, `ensureParameters` created an `AgentgatewayParameters` object
setting the data-plane Service to `ClusterIP`, because agentgateway's default is
`LoadBalancer` and that never gets an address on a cluster with no
load-balancer provider.

The object was created correctly. Nothing referenced it. `ensureGatewayParametersRef`
was a no-op whose comment asserted the overlay was "attached at the GatewayClass
level", but `parametersRef` appeared nowhere in the codebase, and the
GatewayClass on a live cluster had no `spec.parametersRef`. The data-plane
Service was `LoadBalancer` with `EXTERNAL-IP: <pending>`, exactly the state the
overlay existed to prevent.

The unit test covering this asserted that the `AgentgatewayParameters` object
existed and had `type: ClusterIP`. Both were true throughout. The test passed
against a feature that never worked.

## Decision

**Name the Gateway `agentgateway-educates`**, distinct from every Helm release
name this operator installs. The data plane gets its own Deployment, Service and
ServiceAccount under that name, and the control-plane objects are left alone.
The URL handed to sessions follows the Gateway name, so it now resolves to the
data plane.

**Attach `parametersRef` to the Gateway**, at
`spec.infrastructure.parametersRef`, rather than to the GatewayClass.

The GatewayClass is agentgateway's own object: its controller creates it after
leader election, and this operator only waits for it (ADR-0005). Writing a field
into a resource another controller owns invites a write loop in which each side
reasserts its version. The Gateway is this operator's own, so nothing contends
for it. agentgateway applies GatewayClass overlays first and Gateway overlays
second, so a Gateway-level ref also wins outright over anything set
cluster-wide.

**Delete the pre-rename Gateway on sight**, guarded on this operator's
`app.kubernetes.io/managed-by` label. Renaming a Kubernetes object means
creating a new one; the wedged original does not go away by itself, and would
keep logging a rejected apply forever. The label guard means a Gateway called
`agentgateway` that a cluster operator wrote themselves, or that an external
provider owns, is never touched.

## Consequences

A cluster running an earlier build heals on the next reconcile with no manual
step: the wedged Gateway is removed, the correctly-named one is created, and its
data-plane Service comes up as `ClusterIP`.

`GatewayURL` in `AgentGatewayPlatform` status changes value. Nothing persists it
across restarts and sessions re-read it, so no migration is needed, but a
workshop that hardcoded the old URL rather than reading it from the session
Secret would break, correctly, having been pointed at the control plane.

The constant `LegacyGatewayName` is now dead weight that must be carried until
every cluster has upgraded. Its guard clause is written so that setting it equal
to `GatewayName` disables the prune entirely, which is how it should eventually
be retired.

Both defects shared a shape: the operator created an object and assumed the
effect. The tests asserted on the objects, so they passed. The tests added here
assert on the *link*: that the Gateway references the overlay, and that
`GatewayName` differs from the release names: because that is where both bugs
actually lived.

## Notes

The empirical check that established the naming rule is worth repeating when
agentgateway is upgraded, since nothing in the Gateway API spec mandates it:

```sh
kubectl create ns agw-probe
kubectl apply -f - <<'EOF'
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: probe-dataplane
  namespace: agw-probe
spec:
  gatewayClassName: agentgateway
  listeners:
    - name: llm
      port: 4000
      protocol: HTTP
EOF
kubectl get deploy,svc,sa -n agw-probe   # all named probe-dataplane
kubectl delete ns agw-probe
```
