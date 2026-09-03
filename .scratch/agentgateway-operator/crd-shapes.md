# agentgateway v1.5.0 CRD shapes

Verified by reading the CRD schemas shipped in the `agentgateway-crds` 1.5.0
chart. This file exists because several details here contradict `design.md`,
which was written from docs rather than from the schemas.

All kinds: group `agentgateway.dev`, version `v1alpha1`, scope **Namespaced**.

| Kind | plural | short |
|---|---|---|
| AgentgatewayPolicy | agentgatewaypolicies | agpol |
| AgentgatewayModel | agentgatewaymodels | agmodel |
| AgentgatewayParameters | agentgatewayparameters | agpar |
| AgentgatewayBackend | agentgatewaybackends | agbe |

## Corrections to design.md

1. Provider enum is `OpenAI` / `Anthropic` — **not** the design doc's `openAI` /
   `anthropic`. Wrong casing is rejected by CRD validation.
2. The alias mechanism is `spec.virtualModel`; `spec.visibility: Internal` is
   what hides the concrete model. A model is *either* concrete (`provider`) or
   virtual (`virtualModel`) — never both.
3. Credentials must sit on the **concrete** model. CEL forbids `policies` on a
   virtual model, so the alias cannot carry auth.
4. `unit: Tokens` is on the **descriptor**, not the entry, and is a *cost* unit.
   `rateLimit.local[].unit` is a *time* unit — same name, different field.
5. `failureMode` has **no schema default**. Omitting it leaves it absent and only
   the data plane applies FailClosed. Always write it explicitly.

## AgentgatewayPolicy

`spec`: `backend`, `frontend`, `strategy`, `targetRefs`, `targetSelectors`, `traffic`.

### spec.targetRefs[] — no namespace field

`group` (req), `kind` (req), `name` (req), `sectionName?`, `port?`.
`minItems: 1`, `maxItems: 16`. The target must be in the policy's own namespace.
CEL allows only one *kind* of targetRef per policy. Permitted kinds:
Gateway / HTTPRoute / GRPCRoute / ListenerSet (`gateway.networking.k8s.io`),
Service (core `""`), AgentgatewayBackend (`agentgateway.dev`), InferencePool.

A sibling `spec.targetSelectors[]` swaps `name` for a required `matchLabels`.

### spec.traffic.apiKeyAuthentication

```
mode              enum [Optional, Permissive, Strict]   default Strict
configMapSelector.matchLabels   map, required when used
secretSelector.matchLabels      map
secretRef         {name (req), group?, kind? = Secret}
location          exactly one of header{name,prefix} | queryParameter{name}
                  | cookie{name} | expression (CEL)
                  default: Authorization header, "Bearer " prefix
```

CEL: exactly one of `[secretRef secretSelector configMapSelector]`.

A ConfigMap-sourced entry must use `keyHash`; a raw key is rejected. Each `data`
entry is one key, its value JSON:

```json
{"keyHash": "sha256:<hex>", "metadata": {"session": "ws-123"}}
```

### spec.traffic.rateLimit.global

```
domain        string, required, minLength 1
descriptors[] required, 1..16
  entries[]   required, 1..16
    name        string, required, maxLength 64
    expression  string, required, CEL
  unit          enum [Requests, Tokens]   default Requests   <- ON THE DESCRIPTOR
  cost?         CEL
  limitOverride? CEL
backendRef    {name (req), namespace (present), group? = "",
               kind? = Service, port (required when Service + "")}
url           mutually exclusive with backendRef
failureMode   enum [FailClosed, FailOpen]   no schema default
```

CEL: exactly one of `[backendRef url]`; `required: [descriptors, domain]`.

Note the asymmetry ADR-0002 predicted: `targetRefs` has no namespace,
`rateLimit.global.backendRef` does.

## AgentgatewayModel

`spec.required: [parentRefs]`. Keys: `azure`, `baseURL`, `bedrock`, `custom`,
`match`, `parentRefs`, `policies`, `provider`, `vertexai`, `virtualModel`,
`visibility`.

Load-bearing CEL rules:

- exactly one of `[provider virtualModel]`
- `policies` cannot be used with `virtualModel`
- virtual models must be Public
- a virtual model's `match.model` must be exact (no `*`)
- `azure`/`vertexai`/`bedrock`/`custom` set iff `provider` matches
- `Ollama` requires `baseURL`; `baseURL` requires `provider`

Provider enum (20 values):

```
Anthropic, Azure, Baseten, Bedrock, Cerebras, Cohere, Custom, Deepinfra,
Deepseek, Fireworks, Gemini, Groq, Huggingface, Mistral, Ollama, OpenAI,
Openrouter, TogetherAI, VertexAI, XAI
```

Other fields:

```
spec.match.model     upstream model name; omitted => matches metadata.name
                     exact, or one '*' at position 0 or last
spec.visibility      enum [Internal, Public]   default Public
                     "Internal models can only be selected by virtual models"
spec.policies.auth.secretRef  {name (req), key?, group?, kind? = Secret}
spec.parentRefs[]    Gateway API ParentReference; group default
                     gateway.networking.k8s.io, kind default Gateway
```

`spec.virtualModel` — exactly one of `[weighted failover conditional]`:

```
weighted.targets[]     modelRef{name} (req, no namespace), model?, weight (default 1)
failover.targets[]     modelRef{name}, model?, priority (req)
conditional.targets[]  modelRef{name}, model?, when (CEL; omit only on the last)
```

### The catalog render pattern

One catalog entry becomes two objects:

```yaml
# concrete, hidden — carries the credential
spec:
  provider: OpenAI
  visibility: Internal
  match: { model: gpt-4o-mini }        # upstream name
  policies:
    auth:
      secretRef: { name: openai-credentials, key: api-key }
---
# alias — what attendees address
spec:
  visibility: Public
  match: { model: fast }               # catalog name, exact
  virtualModel:
    weighted:
      targets:
        - modelRef: { name: <concrete model> }
          weight: 1
  # no policies block — CEL rejects it
```

## AgentgatewayParameters

`spec.service.spec` is an opaque ServiceSpec
(`x-kubernetes-preserve-unknown-fields`) applied as a strategic merge patch, so
`type: ClusterIP` is written directly with no enum validation.

Other spec fields: `workload.kind` `[DaemonSet|Deployment]`; overlays for
`deployment`, `daemonSet`, `serviceAccount`, `podDisruptionBudget`,
`horizontalPodAutoscaler`; `image{registry,repository,tag,digest,pullPolicy}`;
`env[]`; `resources`; `logging{level,format}`; `shutdown`; `istio`; `spiffe`;
`modelCatalog.sources[].configMap{name,key}`; `rawConfig`.

Overlay order: GatewayClass typed, Gateway typed, GatewayClass overlays, Gateway
overlays. Referenced from `GatewayClass.spec.parametersRef` or
`Gateway.spec.infrastructure.parametersRef`. `modelCatalog` is honoured only at
the Gateway level.

## AgentgatewayBackend

Not needed for the model-based flow — credentials for an `AgentgatewayModel`
live on the model. `AgentgatewayBackend` serves the older
`spec.ai.groups[].providers[]` style.

## Chart provenance

`design.md` and ADR-0005 name `cr.agentgateway.dev` as the chart source; that
registry denies anonymous pulls. The charts are pullable from
`ghcr.io/agentgateway/charts/`:

```
agentgateway-crds 1.5.0  sha256:a6e554e344e4d57e426fd04a9f54e57ea605bc58511d2f65a5626d741e7a73fa
agentgateway      1.5.0  sha256:a22cfb7dcc56da05d31b18c0a969e1b8eee35d19a4493d88b5d4720efcc85945
```
