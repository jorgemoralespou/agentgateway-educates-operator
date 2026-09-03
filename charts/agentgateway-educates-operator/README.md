# agentgateway Educates operator

Gives each Educates workshop attendee their own rate-limited LLM API key,
injected into their session as an environment variable. The workshop author
never handles a credential and the attendee never sees a key value in the
workshop content.

## Before you install: this operator can escalate privileges

**This operator's ServiceAccount holds `bind` and `escalate` on ClusterRoles,
which is approximately cluster-admin equivalent.** Anyone able to create an
`AgentGatewayPlatform`, or to exec into the operator's pod, can use it to grant
themselves arbitrary permissions.

This is not a choice this design made. The operator installs agentgateway's Helm
charts, those charts ship their own RBAC, and Kubernetes forbids creating a role
that grants permissions the creator does not already hold. Any operator that
installs an RBAC-bearing chart needs this. It is stated here plainly rather than
buried, because it should be an informed decision (ADR-0005).

If that is not acceptable, install agentgateway yourself and point this operator
at it with `provider: ExternalAgentgateway`. It then installs nothing and needs
no such permission.

## Installing

```console
helm install agentgateway-educates-operator \
  oci://ghcr.io/educates/charts/agentgateway-educates-operator \
  --namespace educates --create-namespace
```

Then declare the platform and a catalog:

```yaml
apiVersion: agentgateway.operators.educates.dev/v1alpha1
kind: AgentGatewayPlatform
metadata:
  name: cluster
spec:
  provider: BundledAgentgateway
  gatewayAPI: Managed
---
apiVersion: agentgateway.operators.educates.dev/v1alpha1
kind: AgentGatewayCatalog
metadata:
  name: cluster
spec:
  models:
    - name: fast
      provider: OpenAI
      model: gpt-4o-mini
      credentialRef:
        name: openai-credentials
        key: api-key
  # Budgets are enforced per catalog, so the failure mode lives here rather
  # than on the platform. FailClosed refuses a request when the rate limit
  # service is unreachable, instead of letting it through unmetered.
  rateLimit:
    failureMode: FailClosed
```

The credential Secret is yours to create, in the gateway namespace
(`agentgateway-system` by default). No credential material ever appears in a
custom resource, so both of the above are safe to commit.

## Values

| Key | Default | Description |
|---|---|---|
| `image.repository` | *(from Chart annotations)* | Full image repository including registry, no tag. |
| `image.tag` | `.Chart.AppVersion` | Image tag. |
| `image.pullPolicy` | *(derived)* | `Always` for a floating tag, else `IfNotPresent`. |
| `development.imageRegistry` | `{}` | `{host, namespace}`, to override just the registry prefix. |
| `imagePullSecrets` | `[]` | Pull secrets for a private registry. |
| `leaderElection.enabled` | `true` | Prevents two overlapping pods converging during a rollout. |
| `gatewayAPI.install` | `true` | Set `false` when an ingress controller already owns the Gateway API CRDs. |
| `resources` | `{}` | Container resources. |
| `nodeSelector`, `tolerations`, `affinity` | `{}` | Scheduling. |

There is deliberately no passthrough of agentgateway chart values: every knob is
a typed field on the custom resources, mapped by hand. The chart versions are
compile-time constants in the operator image, so **upgrading agentgateway means
upgrading this operator**. Users needing a different version use
`ExternalAgentgateway`.

## Uninstalling

```console
kubectl delete agentgatewayplatform cluster
helm uninstall agentgateway-educates-operator --namespace educates
```

Delete the platform declaration first and let it finish. It uninstalls in strict
reverse install order; removing the operator first would leave the releases
behind with nothing left to drain them.

CRDs are in the chart's `crds/` directory, which Helm installs but never removes
on uninstall. Delete them by hand if you want them gone.

The Gateway API CRDs this chart installs when `gatewayAPI.install` is true are
also left behind, by an explicit `helm.sh/resource-policy: keep` — removing
Gateway API from under an ingress controller that started using it would be
worse than leaving two CRDs in place (ADR-0006). Delete them by hand only once
nothing else on the cluster needs them.
