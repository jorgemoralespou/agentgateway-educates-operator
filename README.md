# agentgateway Educates operator

Gives each [Educates](https://educates.dev) workshop attendee their own
rate-limited LLM API key, injected into their session as an environment
variable.

The workshop author writes one resource. The attendee gets a working key with a
token budget, revoked when their session ends. Neither ever handles a
credential.

```yaml
session:
  objects:
    - apiVersion: agentgateway.operators.educates.dev/v1alpha1
      kind: AgentGatewaySession
      metadata:
        name: $(session_name)
        namespace: $(workshop_namespace)
      spec:
        tokenBudget: 100000
        ttl: 4h
  env:
    - name: OPENAI_API_KEY
      valueFrom:
        secretKeyRef:
          name: $(session_name)-agentgateway
          key: api-key
    - name: OPENAI_BASE_URL
      valueFrom:
        secretKeyRef:
          name: $(session_name)-agentgateway
          key: base-url
```

`OPENAI_API_KEY` and `OPENAI_BASE_URL` are what every OpenAI-compatible client
library reads by default, so workshop content needs no gateway-specific code.

## Why

A workshop that uses an LLM has an awkward choice: hand every attendee the same
shared key, or make each of them sign up for their own. The first means one
runaway loop can exhaust the budget for the whole room and there is no way to
tell who did it. The second turns the first twenty minutes of a workshop into
account creation.

This operator takes the third option — a real key per attendee, minted at
session start, budgeted independently, and revoked at session end — and makes
it the author's default rather than a project of its own.

## What it installs

Three custom resources, in `agentgateway.operators.educates.dev/v1alpha1`:

| Kind | Scope | What it is |
|---|---|---|
| `AgentGatewayPlatform` | Cluster singleton `cluster` | Installs and owns agentgateway itself. |
| `AgentGatewayCatalog` | Cluster singleton `cluster` | The models workshops may use, and the credentials behind them. |
| `AgentGatewaySession` | Namespaced | What a workshop author writes into `session.objects`. |

A cluster operator writes the first two once. Workshop authors only ever write
the third.

Models are addressed by **catalog name** — `fast`, `smart` — not by a
provider's model name. Which upstream model each resolves to is a cluster-level
decision, so re-pointing `fast` at a different provider is a catalog edit and
touches no workshop.

## How a key is made and unmade

Each session's credential exists as two objects in two namespaces, with two
different lifecycles.

The **Secret** holds the plaintext key and lives in the *workshop* namespace,
where the attendee's pod can resolve a `secretKeyRef`. It is owned by the
*session* namespace, so Kubernetes garbage-collects it when the session ends —
no controller has to be running for revocation to happen.

The **registration** lives in the gateway's namespace and holds only
`sha256:<hex>` of the key. The gateway authenticates by hashing what it
receives. Nothing on the gateway side can reproduce a key, which is why that
object is an ordinary ConfigMap. It is owned by nothing, so a finalizer removes
it, and a TTL on the grant is the backstop behind both.

The key never appears in any resource's `status`.

## Getting started

Installing, credentials, the two Gateway API paths, upgrades and troubleshooting
are in **[the deployment guide](docs/deployment.md)**. The short version:

```console
helm install agentgateway-educates-operator \
  oci://ghcr.io/educates/charts/agentgateway-educates-operator \
  --namespace educates --create-namespace
```

Then declare a platform and a catalog, and create the Secret holding your
provider credential. The guide walks through each.

> **Before you install**, read [the privilege-escalation
> note](charts/agentgateway-educates-operator/README.md#before-you-install-this-operator-can-escalate-privileges).
> Installing agentgateway's charts requires `bind` and `escalate` on
> ClusterRoles, which is approximately cluster-admin equivalent. There is an
> `ExternalAgentgateway` path that needs no such permission.

## Try it

[`sample-workshop/`](sample-workshop/) is a four-page Educates workshop that
demonstrates the whole thing: get a key, see where it came from, exhaust a
budget, watch it be revoked. It doubles as the operator's living documentation
— the `session.objects` snippet it shows attendees is its own.

## Runs on amd64 and arm64

Released images are manifest lists covering `linux/amd64` and `linux/arm64`, so
no architecture-specific values, node selectors or affinity rules are needed on
either.

## Documentation

- [Deployment guide](docs/deployment.md) — install, configure, upgrade, uninstall.
- [Chart reference](charts/agentgateway-educates-operator/README.md) — values, RBAC, uninstall order.
- [Sample workshop](sample-workshop/README.md).
- [Glossary](CONTEXT.md) — the vocabulary this project uses, and the terms it avoids.
- [Live-workshop validation runbook](docs/validation/workshop-validation.md).
- Architecture decisions: [ADR index](docs/adr/).

## Development

```console
make ci-operator   # everything CI runs: vet, build, drift checks, envtest, lint
make ci-e2e        # kind cluster, chart install, cascading-deletion e2e
make help          # every target
```

CI is the same single Make target, so local and CI cannot drift. `make test-e2e`
alone runs the e2e against a cluster you already have.
