# agentgateway Educates operator, design

Status: design agreed, not yet built. Targets **Educates v4** (`develop`).

A Go operator that lets an Educates workshop give each attendee an LLM API key,
scoped and rate-limited, injected into their session as an environment variable.
Terms are defined in `/CONTEXT.md`; the four hard-to-reverse decisions are in
`/docs/adr/`.

## What an author writes

The whole integration surface, in a Workshop definition:

```yaml
spec:
  session:
    env:
      - name: OPENAI_BASE_URL
        valueFrom:
          secretKeyRef: { name: $(session_name)-agentgateway, key: base-url }
      - name: OPENAI_API_KEY
        valueFrom:
          secretKeyRef: { name: $(session_name)-agentgateway, key: api-key }
    objects:
      - apiVersion: agentgateway.operators.educates.dev/v1alpha1
        kind: AgentGatewaySession
        metadata:
          name: $(session_name)
          namespace: $(workshop_namespace)   # REQUIRED, see ADR-0002
        spec:
          catalogRef: { name: default }
          tokenBudget: 100000
```

`namespace: $(workshop_namespace)` is load-bearing and easy to omit. Without it
the resource lands in the session namespace, the Secret follows it there, and the
attendee's pod cannot resolve the `secretKeyRef`. The operator should reject a
resource in the session namespace with a clear message rather than let the pod
wedge in `CreateContainerConfigError`.

## Custom resources

Group `agentgateway.operators.educates.dev`, version `v1alpha1`. The
`operators.` infix marks third-party operators as peers of the platform, distinct
from the runtime's `training.educates.dev` and the v4 installer's
`platform.educates.dev`.

### AgentGatewayCatalog (cluster-scoped, singleton `cluster`)

Declares the models the Gateway offers. Renders agentgateway's own resources with
defaults applied; it does **not** install a Gateway.

```yaml
apiVersion: agentgateway.operators.educates.dev/v1alpha1
kind: AgentGatewayCatalog
metadata:
  name: cluster
spec:
  gateway:
    name: agentgateway-proxy
    namespace: agentgateway-system
  models:
    - name: fast                     # what attendees address
      provider: openAI
      model: gpt-4o-mini             # never seen by attendees
      credentialRef:
        name: openai-credentials     # a Secret name, never key material
        key: api-key
    - name: smart
      provider: anthropic
      model: claude-sonnet-4-0
      credentialRef: { name: anthropic-credentials, key: api-key }
```

Every entry renders as an internal model plus a virtual-model alias, so attendees
address `fast` and never learn the provider or upstream model behind it. Changing
that binding is a catalog edit, not a workshop-content edit.

Deliberately omitted, per "simplicity over configurability": failover, weighted
routing, per-model overrides. All are additive later.

Gates on `EducatesClusterConfig.status` (never `.spec`) for ingress and image
registry, per the v4 contract, and surfaces that as a
`ClusterConfigAvailable` condition.

### AgentGatewaySession (namespaced)

One per attendee, created by Educates from `session.objects`.

```yaml
spec:
  catalogRef: { name: cluster }
  tokenBudget: 100000        # tokens for the session's lifetime
  ttl: 4h                    # backstop expiry, see ADR-0002
status:
  phase: Ready               # advisory
  conditions: [...]          # authoritative: Ready, KeyRegistered, SecretWritten
  observedGeneration: 2
  secretRef: { name: ws-123-agentgateway }
  gatewayURL: http://agentgateway.agentgateway-system.svc.cluster.local:4000
```

Status never carries the key or its hash. Printer columns on Phase and Age.

## What reconcile does

Two objects per session, in two namespaces:

```
AgentGatewaySession (workshop ns)
  ├─► Secret <name>-agentgateway        (workshop ns) , plaintext key + base URL
  │      owned by the session namespace, cascades on teardown
  └─► ConfigMap <name>-agentgateway     (gateway ns)  , sha256 hash + metadata
         owned by nothing; removed by finalizer (ADR-0002)
```

Ordinary pass:

1. Resolve the catalog; if not Ready, set `ClusterConfigAvailable=False` and requeue.
2. If the Secret is absent, generate a `sk-`-prefixed crypto-random key. **Only
   then**: a key on every pass would rotate the attendee out of their session.
3. Write the Secret: `api-key`, `base-url`.
4. Write the registration ConfigMap to the gateway namespace, labelled to match
   the policy selector, holding `sha256:<hex>` and `{"session": "<name>"}`.
5. Set conditions; `Ready` last.

Deletion: bounded finalizer. Retry the ConfigMap delete for a fixed window, then
log loudly, release, and let the namespace go. Never block indefinitely.

### Key registration shape

One ConfigMap per session, not one shared map with an entry per attendee, thirty
attendees starting at once would otherwise contend on a single hot object, and
duplicate keys across ConfigMaps are documented as undefined behaviour.

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: ws-123-agentgateway
  namespace: agentgateway-system     # MUST equal the policy's namespace
  labels: { agentgateway.dev/apikey: "true" }
data:
  ws-123: |
    {"keyHash": "sha256:9f2b...", "metadata": {"session": "ws-123"}}
```

The `data` key name is an arbitrary identifier: the controller iterates the map
and uses the name only in error messages. ConfigMaps reject raw keys, which is
exactly the constraint we want.

## Rate limiting

A single `AgentgatewayPolicy` in the gateway namespace, selecting every
registration ConfigMap by label. One policy, cluster-wide: two policies on one
Gateway silently overwrite each other (ADR-0002).

```yaml
traffic:
  apiKeyAuthentication:
    mode: Strict
    configMapSelector:
      matchLabels: { agentgateway.dev/apikey: "true" }
  rateLimit:
    global:
      domain: agentgateway
      backendRef: { kind: Service, name: ratelimit, namespace: ratelimit, port: 8081 }
      descriptors:
        - entries:
            - name: session
              expression: 'apiKey.session'   # flattened, NOT apiKey.metadata.session
          unit: Tokens
```

`backendRef` does take a namespace: the cross-namespace restriction is specific
to the credential selector.

## Packaging

One Helm chart, matching the v4 installer's conventions: no `config/` kustomize
tree, `controller-gen` writes CRDs and RBAC straight into the chart. The chart
ships agentgateway, `envoyproxy/ratelimit`, and a persistence-free Redis
(`redis:7-alpine`, no PVC, counters reset on restart, which is fine).

This operator is a **peer** of `educates-installer`, not a plugin: v4's installer
installs a hard-coded list of embedded charts and has no third-party extension
point.

House style from `installer/operator/`: the only Go operator in the Educates
org, and the reference:

- kubebuilder v4, multigroup layout, Go 1.26.3, controller-runtime v0.24.1
- `metav1.Condition` with `+listType=map`/`+listMapKey=type`, authoritative;
  typed `Phase` enum advisory alongside it. **Not** `status.educates.*`, which is
  a kopf artefact and inappropriate for a Go operator.
- Cluster-scoped singletons named `cluster`, enforced by a CEL `XValidation` rule
- `groupversion_info.go` hand-rolls its SchemeBuilder rather than importing
  controller-runtime: a considered deviation from the scaffold, worth matching
- Distroless nonroot image; CI is one step, `make ci-operator`
- Single replica, leader election enabled

## Deliberately not doing

- **Cumulative per-user cost budgets.** Needs a database (ADR-0001, ADR-0003).
- **`request.objects`.** Warranted only with reserved sessions, which are not in
  use. Would also give `$(username)`/`$(email)` for attribution, revisit if
  reserved sessions arrive.
- **Model failover and weighted routing.** Additive later.
- **A Gateway installer CRD.** Helm installs the Gateway; the catalog configures
  it. This was the shape the LiteLLM prior art tried and abandoned.

## Before writing code

**Validate the namespace assumption on a throwaway workshop.** The workshop-vs-
session namespace behaviour is read from Educates source, not observed. It is the
single highest-risk assumption here: if wrong, every session breaks identically.
Test startup *and* teardown, since teardown exercises the finalizer path.

Also worth an early check: `failureMode` on the rate-limit policy (ADR-0003),
decide whether a rate-limit outage should stop LLM traffic or silently remove
enforcement.

## Provenance

Verified against source, not just docs:

| Claim | Source |
|---|---|
| Workshop Deployment in workshop ns; session objects default to session ns | `workshopsession.py:1556`, `:1262` @ `d7bf2058` |
| Session objects owned by the session *namespace*, not the WorkshopSession CR | `workshopsession.py:1283` (published docs are stale) |
| No user/identity substitution variables exist | `workshopsession.py:988-1018`, byte-identical to 3.5.1 |
| Credential selector pinned to the policy's namespace | `plugins/traffic_plugin.go:945,951` |
| API-key policies replace rather than merge | `store/policy.rs:266-284` |
| Token limits work over remote RLS | `remoteratelimit.rs:137-179`, `rls.proto:239-241` |
| CEL metadata is flattened onto `apiKey` | `http/apikey.rs:53-60` |
| ConfigMaps reject raw keys | `plugins/traffic_plugin.go:983` |
| Educates runtime stays Python/kopf in v4 | v4 `AGENTS.md` |

Assumed, not verified: that the namespace behaviour is deliberate rather than an
unfixed bug (an `XXX` comment at `workshopsession.py:1240` suggests the authors
know); and end-to-end behaviour of the whole flow against a live v4 cluster.
