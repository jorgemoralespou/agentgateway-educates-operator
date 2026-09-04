# Deployment guide

Installing and operating the agentgateway Educates operator. For what it is and
why, see the [README](../README.md). For the full values table and RBAC detail,
see the [chart reference](../charts/agentgateway-educates-operator/README.md),
this guide links to it rather than repeating it.

## Contents

- [Prerequisites](#prerequisites)
- [Before you install: privilege escalation](#before-you-install-privilege-escalation)
- [Gateway API: choose a path](#gateway-api-choose-a-path)
- [Installing the operator](#installing-the-operator)
- [Declaring the platform](#declaring-the-platform)
- [Providing a credential](#providing-a-credential)
- [Declaring the catalog](#declaring-the-catalog)
- [Verifying](#verifying)
- [Using it from a workshop](#using-it-from-a-workshop)
- [Exposing the gateway UI](#exposing-the-gateway-ui)
- [Upgrading](#upgrading)
- [Uninstalling](#uninstalling)
- [Troubleshooting](#troubleshooting)

## Prerequisites

- **Kubernetes 1.32 or later.** agentgateway 1.5.x supports 1.32–1.37; Educates
  v4 requires 1.34+. The chart declares `kubeVersion: ">=1.32.0-0"`.
- **Helm 3.8 or later**, for OCI registry support.
- **An Educates cluster**, if you intend to use this from workshops. The
  operator runs without Educates: it reads `EducatesClusterConfig` if present
  and treats its absence as normal, but a session grant only makes sense
  inside a workshop.
- **An LLM provider account** and an API key. Nothing here provisions one.
- **amd64 or arm64.** Released images are manifest lists covering both; no
  node selector or affinity is needed for either.

## Before you install: privilege escalation

The operator's ServiceAccount holds `bind` and `escalate` on ClusterRoles,
which is approximately cluster-admin equivalent. Anyone who can create an
`AgentGatewayPlatform`, or exec into the operator's pod, can use it to grant
themselves arbitrary permissions.

This is not a design choice so much as a consequence: the operator installs
agentgateway's Helm charts, those charts ship their own RBAC, and Kubernetes
forbids creating a role granting permissions the creator does not hold
(ADR-0005).

If that is unacceptable in your cluster, install agentgateway yourself and set
`provider: ExternalAgentgateway` on the platform. The operator then installs
nothing and needs no such permission. See
[the chart reference](../charts/agentgateway-educates-operator/README.md#before-you-install-this-operator-can-escalate-privileges).

## Gateway API: choose a path

agentgateway is a Gateway API implementation, so the
`gateway.networking.k8s.io` CRDs must exist. There are two supported
arrangements and they are equally valid, but the settings must agree, and
getting that wrong is the most common way to end up with a platform that never
becomes ready.

### Path A: the chart installs Gateway API (default)

For a cluster that does not already have Gateway API. Nothing to configure:

```console
helm install agentgateway-educates-operator \
  oci://ghcr.io/educates/charts/agentgateway-educates-operator \
  --namespace educates --create-namespace
```

The chart installs `GatewayClass` and `Gateway` (standard channel), and the
platform's default `gatewayAPI: Managed` proceeds once they are established.

These CRDs are annotated `helm.sh/resource-policy: keep`, so `helm uninstall`
leaves them behind (ADR-0006).

### Path B: the cluster already owns Gateway API

For a cluster where an ingress controller, Contour, Istio, NGINX Gateway
Fabric, already installs and owns the Gateway API CRDs. This operator must not
fight it.

**Two settings, and they must match.** On the chart:

```console
helm install agentgateway-educates-operator \
  oci://ghcr.io/educates/charts/agentgateway-educates-operator \
  --namespace educates --create-namespace \
  --set gatewayAPI.install=false
```

And on the platform declaration:

```yaml
spec:
  gatewayAPI: Existing
```

Setting only the first leaves the platform in `Managed`, waiting for CRDs
nobody will install. If the platform sits in `Installing` with
`GatewayAPIReady=False`, this is almost always why.

Check what you have before choosing:

```console
kubectl get crd gateways.gateway.networking.k8s.io \
  -o jsonpath='{.metadata.annotations.gateway\.networking\.k8s\.io/bundle-version}{"\n"}'
```

Anything in the 1.4–1.6 range works with agentgateway 1.5.x.

## Installing the operator

From the OCI registry, pinning a version:

```console
helm install agentgateway-educates-operator \
  oci://ghcr.io/educates/charts/agentgateway-educates-operator \
  --version 0.1.0 \
  --namespace educates --create-namespace
```

Or from a release tarball, if you prefer to inspect it first:

```console
helm install agentgateway-educates-operator \
  ./agentgateway-educates-operator-0.1.0.tgz \
  --namespace educates --create-namespace
```

Or from a checkout, for development:

```console
helm install agentgateway-educates-operator \
  ./charts/agentgateway-educates-operator \
  --namespace educates --create-namespace
```

Values are documented in
[the chart reference](../charts/agentgateway-educates-operator/README.md#values).
The ones that matter most often: `image.repository` and `image.tag` to run a
build from elsewhere, `gatewayAPI.install` as above, and `resources`, the
chart sets none, so the operator runs burstable by default.

## Declaring the platform

A cluster singleton, named `cluster`:

```yaml
apiVersion: agentgateway.operators.educates.dev/v1alpha1
kind: AgentGatewayPlatform
metadata:
  name: cluster
spec:
  provider: BundledAgentgateway
  gatewayAPI: Managed
  bundledAgentgateway:
    namespace: agentgateway-system
```

`provider: BundledAgentgateway` has the operator install agentgateway from
charts embedded in its own image (ADR-0005). Upgrading agentgateway means
upgrading this operator; that coupling is deliberate.

To use a gateway you installed yourself:

```yaml
spec:
  provider: ExternalAgentgateway
  gatewayAPI: Existing
  externalAgentgateway:
    namespace: my-gateway-ns
    gatewayName: my-gateway
```

Installation is ordered and each step gates the next: Gateway API CRDs →
agentgateway CRDs → control plane → GatewayClass → Gateway → policy →
readiness. Watch it with:

```console
kubectl get agentgatewayplatform cluster -w
```

## Providing a credential

Create a Secret in the gateway namespace holding your provider API key. This is
the one thing no manifest in this project can do for you:

```console
kubectl create secret generic agentgateway-provider-credentials \
  --namespace agentgateway-system \
  --from-literal=api-key=sk-ant-...
```

The key name (`api-key`) is the default the catalog expects; override it with
`credentialRef.key`.

No credential material ever appears in a custom resource, so platform and
catalog manifests are safe to commit.

## Declaring the catalog

Also a cluster singleton named `cluster`. This is where the models attendees
may use are declared:

```yaml
apiVersion: agentgateway.operators.educates.dev/v1alpha1
kind: AgentGatewayCatalog
metadata:
  name: cluster
spec:
  models:
    - name: fast
      provider: Anthropic
      model: claude-haiku-4-5-20251001
      credentialRef:
        name: agentgateway-provider-credentials
        key: api-key
    - name: smart
      provider: Anthropic
      model: claude-sonnet-4-5-20250929
      credentialRef:
        name: agentgateway-provider-credentials
        key: api-key
  rateLimit:
    failureMode: FailClosed
```

`name` is what attendees address. The provider's own model name is never
exposed to them, so re-pointing `fast` at a different provider is a catalog
edit and no workshop changes.

`failureMode: FailClosed` (the default) refuses requests when the rate limit
service is unreachable rather than letting them through unmetered. For a
workshop handing out budgeted keys, that is the safer failure.

`requestTimeout` is optional and usually omitted. Left unset, agentgateway's
own default applies, which suits every hosted provider. Raise it for a slow
local model:

```yaml
spec:
  models: [...]
  requestTimeout: 180s
```

A reasoning model served by Ollama on a laptop can take minutes over a single
reply, and past the default the attendee sees `upstream call failed: connection
closed before message completed`, which reads like a broken gateway rather
than a slow model. The value is a Go duration (`90s`, `3m`); anything else is
rejected when you apply the catalog. It applies to every model in the catalog,
since one policy governs the Gateway.

Supported providers: Anthropic, Azure, Baseten, Bedrock, Cerebras, Cohere,
Deepinfra, Deepseek, Fireworks, Gemini, Groq, Huggingface, Mistral, Ollama,
OpenAI, Openrouter, TogetherAI, VertexAI, XAI. Ollama also requires `baseURL`.

A ready-to-adapt catalog, with commented OpenAI and Ollama variants, is in
[`sample-workshop/catalog/`](../sample-workshop/catalog/agentgatewaycatalog.yaml).

## Verifying

Both singletons should report `Ready`:

```console
kubectl get agentgatewayplatform cluster
kubectl get agentgatewaycatalog cluster
```

The catalog's `Models` column lists the names workshops may reference. If
either is not ready, its conditions say which step is outstanding:

```console
kubectl get agentgatewayplatform cluster -o jsonpath='{range .status.conditions[*]}{.type}{"\t"}{.status}{"\t"}{.message}{"\n"}{end}'
```

For an end-to-end check against a live workshop, follow
[the validation runbook](validation/workshop-validation.md).

## Using it from a workshop

In the workshop definition, one object and two environment variables:

```yaml
session:
  objects:
    - apiVersion: agentgateway.operators.educates.dev/v1alpha1
      kind: AgentGatewaySession
      metadata:
        name: $(session_name)
        namespace: $(workshop_namespace)   # required
      spec:
        catalogRef:
          name: cluster
        tokenBudget: 100000
        ttl: 4h
  env:
    - name: OPENAI_BASE_URL
      valueFrom:
        secretKeyRef:
          name: $(session_name)-agentgateway
          key: base-url
    - name: OPENAI_API_KEY
      valueFrom:
        secretKeyRef:
          name: $(session_name)-agentgateway
          key: api-key
```

**`namespace: $(workshop_namespace)` is required.** Without it the grant lands
in the session namespace, where the Secret it creates cannot be resolved by the
attendee's pod: that pod runs in the workshop namespace and a `secretKeyRef`
only resolves within its own namespace. The operator rejects a misplaced grant
with `PlacementValid=False` and a message naming the fix, rather than failing
silently.

`tokenBudget` defaults to 100000 and `ttl` to `4h`.

A complete working example is in [`sample-workshop/`](../sample-workshop/).

## Exposing the gateway UI

agentgateway serves a debugging UI at `/ui` on port 15000 of the data plane,
showing the gateway's live routes and traffic. It is off by default and turning
it on is a deliberate decision, for the reasons below.

```yaml
apiVersion: agentgateway.operators.educates.dev/v1alpha1
kind: AgentGatewayPlatform
metadata:
  name: cluster
spec:
  adminInterface:
    exposed: true
```

**This interface has no authentication and never will**, which is upstream's own
position, not a gap in this operator. agentgateway therefore binds it to
loopback, and the operator's default leaves it there. Setting `exposed: true`
rebinds it to all interfaces, which makes it reachable by anything that can
route to the pod. Weigh that before enabling it on a shared cluster:

- The view is **cluster-wide and shared**. Every attendee who reaches it sees
  the same gateway, not their own session. It is a teaching aid, not isolation.
- `POST /quitquitquit` **shuts the data plane down for every attendee at once**.
  Anything that can reach port 15000 can call it.
- Participant keys are not exposed. They are held as hashes in the gateway
  namespace ([ADR-0002](adr/0002-split-namespace-key-model.md)), and in
  Kubernetes mode the data plane takes its configuration over xDS, so the
  local-config endpoints are empty.

The data-plane Service already publishes port 15000: agentgateway derives the
Service ports from the listener set, so nothing has to be added there. Until
this field is set, connections to that port are refused rather than hanging.

If you only need the UI yourself, prefer a port-forward and leave the field off:

```console
kubectl port-forward -n agentgateway-system svc/agentgateway-educates 15000:15000
```

### Reaching it from a workshop

A dashboard tab alone is not enough. Educates loads a dashboard `url` in the
attendee's browser, which cannot resolve a ClusterIP, so the UI has to be
proxied through the workshop dashboard with `session.ingresses`:

```yaml
session:
  ingresses:
    - name: agentgateway
      protocol: http
      host: agentgateway-educates.agentgateway-system.svc.$(cluster_domain)
      port: 15000
  dashboards:
    - name: Gateway
      url: $(ingress_protocol)://agentgateway-$(session_name).$(ingress_domain)/ui/
```

The ingress keeps Educates' own session authentication in front of the UI, so an
attendee who is logged into their session reaches it and nobody else does. That
is the only access control here; it does not make the view per-attendee.

Do not give this ingress a `path`. The UI is a single-page app whose base is
pinned to `/ui/` with its API calls rooted at `/api/`, and it has no runtime base
path, so it only works proxied at the root of its own hostname. Scoping it to a
subpath serves the HTML and then 404s every asset.

The `host` above assumes the default `bundledAgentgateway.namespace`. A platform
installed elsewhere needs it changed to match.

## Upgrading

```console
helm upgrade agentgateway-educates-operator \
  oci://ghcr.io/educates/charts/agentgateway-educates-operator \
  --version <new> --namespace educates
```

Two things to know.

**Upgrading the operator upgrades agentgateway.** The chart versions are
compile-time constants in the operator image (ADR-0005), so a new operator
release may converge agentgateway to a new version on its next reconcile.
Release notes call this out when it happens.

**CRDs in `crds/` are not upgraded by Helm.** If a release changes the
operator's own CRD schemas, apply them by hand:

```console
kubectl apply -f https://raw.githubusercontent.com/educates/agentgateway-educates-operator/<tag>/charts/agentgateway-educates-operator/crds/
```

The Gateway API CRDs are templated rather than in `crds/`, so those *are*
upgraded by `helm upgrade` (ADR-0006).

Existing sessions keep working across an operator upgrade: their keys are
already minted and registered, and reconcile is idempotent.

## Uninstalling

Order matters:

```console
kubectl delete agentgatewayplatform cluster   # wait for it to finish
helm uninstall agentgateway-educates-operator --namespace educates
```

Delete the platform first and let it complete. It uninstalls in strict reverse
install order and drains what it installed; removing the operator first leaves
those releases behind with nothing left to drain them.

Left behind on purpose:

- **The operator's own CRDs**, in `crds/`, Helm never removes those.
- **The Gateway API CRDs**, by an explicit `resource-policy: keep`. Removing
  Gateway API from under an ingress controller that adopted it would be worse
  than leaving them (ADR-0006).

Delete either by hand once you are sure nothing else needs them.

## Troubleshooting

### The platform sits in `Installing`

Check `GatewayAPIReady`:

```console
kubectl get agentgatewayplatform cluster -o jsonpath='{range .status.conditions[*]}{.type}{"\t"}{.status}{"\t"}{.message}{"\n"}{end}'
```

If it is `False` with a message about CRDs not being present, the Gateway API
settings disagree, see [Gateway API: choose a path](#gateway-api-choose-a-path).
Either the chart was installed with `gatewayAPI.install=false` while the
platform says `Managed`, or the CRDs were removed after installation.

If you install Gateway API by hand while the operator is already running, it
picks them up without a restart.

### A session is `Rejected`

`PlacementValid=False` means the grant is in the session namespace instead of
the workshop namespace. Add `namespace: $(workshop_namespace)` to the object in
`session.objects`. This is deliberately not retried: it needs an author fix,
not a requeue.

### An attendee gets 401

The key is not being matched by the gateway's policy. Confirm both halves
exist:

```console
kubectl get secret "$SESSION-agentgateway" -n "$WORKSHOP_NAMESPACE"
kubectl get configmap "$SESSION-agentgateway" -n agentgateway-system
```

If the Secret exists but the registration does not, the session did not reach
`Ready`, check its conditions.

### An attendee gets 404 on the model

They asked for a name that is not in the catalog. `kubectl get
agentgatewaycatalog cluster` lists the available names in its `Models` column.

### An attendee gets 429 immediately

Their budget is exhausted. Budgets are per session; a new session gets a new
one. Note that rate limiting runs *before* prompt guards, so requests rejected
by a guardrail still consume budget.

### An attendee gets `upstream call failed: connection closed before message completed`

The model took longer to answer than the gateway was willing to wait. Common
with a reasoning model served locally: it is working, just slowly, and nothing
in the platform or catalog status reports a problem.

Confirm by calling the provider directly, bypassing the gateway, and timing it:

```console
curl -s -o /dev/null -w '%{http_code} %{time_total}s\n' \
  -X POST http://<your-ollama>:11434/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"<model>","messages":[{"role":"user","content":"hi"}],"stream":false}'
```

A `200` after a long wait means the model is fine and the timeout is the
limit, raise `requestTimeout` on the catalog. A failure there is a problem
with the model or its address, not with this operator.

### The gateway UI tab shows a connection error

The admin listener is bound to loopback, which is the default. Set
`adminInterface.exposed: true` on the platform, see
[Exposing the gateway UI](#exposing-the-gateway-ui). The port is refused rather
than empty until then, so the tab reports a connection failure instead of
rendering a blank page.

Confirm which state the listener is in from the data plane's own log:

```console
kubectl logs -n agentgateway-system -l gateway.networking.k8s.io/gateway-name=agentgateway-educates \
  | grep 'component="admin"'
```

`address=127.0.0.1:15000` means loopback, so the field is off or has not
reconciled yet. `address=[::]:15000` means it is exposed and the problem is
elsewhere, most likely the ingress `host` naming the wrong namespace.

If the data-plane pod is crash-looping instead, check for a hand-edited
`AgentgatewayParameters`: agentgateway rejects `adminAddr` at the top level of
`rawConfig` and exits, where the operator writes it under `rawConfig.config`.

### The gateway UI tab renders unstyled, or its panels stay empty

The ingress has a `path`. The UI is pinned to `/ui/` with its API at `/api/`, so
proxying it under a subpath serves the HTML and 404s every asset. Remove `path`
and `pathRewrite` and give it its own hostname.

### Leaked registrations

If a cleanup gave up, the operator logs loudly when it does, the runbook has
a [recovery procedure](validation/workshop-validation.md#recovering-leaked-registrations)
using label selectors.
