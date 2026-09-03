# Sample workshop

A four-page Educates workshop demonstrating what this operator does: every
attendee gets their own budgeted LLM API key, minted when their session starts
and revoked when it ends.

It doubles as living documentation. The `session.objects` snippet on page 2 is
the same one in `resources/workshop.yaml`, so the workshop shows an author
exactly what to write.

## Layout

```
resources/workshop.yaml         the Workshop definition
resources/trainingportal.yaml   a portal with capacity for two sessions
workshop/content/*.md           the four pages, ordered by filename prefix
catalog/agentgatewaycatalog.yaml  the model catalog the workshop reads from
```

`catalog/` is deliberately outside `resources/`: it is cluster setup the
workshop depends on, not an Educates workshop resource. The CI publisher copies
only `workshop.yaml` and `trainingportal.yaml`.

## The pages

1. **Your LLM key** — the environment variables are already set; `curl` the
   gateway and get a completion.
2. **Where your key came from** — the resource the author wrote, the grant and
   Secret the operator produced, and the hash-only registration on the gateway
   side.
3. **Your budget is yours alone** — exhaust a deliberately small budget, watch
   the 429s, confirm a neighbour is unaffected.
4. **When your session ends** — the owner reference, the finalizer and the TTL
   that revoke the key.

## Prerequisites

A cluster running Educates with this operator installed, a ready
`AgentGatewayPlatform`, and the catalog applied with a real provider
credential. See [the deployment guide](../docs/deployment.md).

## Running it locally

Against a local cluster from `educates create-cluster`:

```console
make deploy-sample-catalog   # checks the credential Secret exists first
make publish-workshop        # pushes to the cluster's registry
make deploy-workshop         # creates a training portal and prints its URL
```

`make undeploy-workshop` removes the workshop from the portal. The portal
itself survives; use `educates delete-portal` for that.

These targets need the `educates` CLI on `PATH`.

## Attendee RBAC

Pages 2 to 4 run `kubectl` against the attendee's own grant and Secret. The
default Educates session role does not cover custom resources, so
`resources/workshop.yaml` declares a `Role` and `RoleBinding` in
`session.objects` granting read access to them. Remove those and the content
fails with permission errors.

Both live in the **workshop** namespace, and the `RoleBinding` subject uses
`$(workshop_namespace)` — that is where the session's service account is, not
the session namespace.

## Publishing

CI publishes this on every push to `main` that touches `sample-workshop/`,
using `educates/educates-github-actions/publish-workshop`. The image lands at
`ghcr.io/<owner>/lab-agentgateway-sample-files` under a single moving tag
(`main`, which is what the action derives from the branch ref — it offers no
way to name it `latest`). The workshop is not versioned alongside operator
releases: it tracks `main` so it always demonstrates current behaviour.
