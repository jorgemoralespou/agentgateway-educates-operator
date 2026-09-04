# 1. Use agentgateway's Kubernetes mode, not standalone

Date: 2026-09-03

## Status

Accepted

## Context

agentgateway ships two products with different configuration models and,
critically, different mechanisms for issuing API keys.

**Standalone** is a single process configured by a YAML file. Keys are managed
through an admin REST API at port 15000. That API requires
`config.storage.mode: hybrid` plus a `config.database.url`, so a database, and
therefore either SQLite on a volume or a Postgres. The admin port is
**unauthenticated**; the docs are explicit that it must stay bound to localhost.
Key propagation is asynchronous, so a caller must retry on 401 immediately after
creating a key.

**Kubernetes mode** is driven by Gateway API plus agentgateway's own CRDs. Keys
are plain ConfigMaps or Secrets, selected by label. No database, no admin port,
no propagation race: a Kubernetes informer watches the objects and pushes a new
xDS snapshot on change.

Educates workshop clusters are ephemeral and the platform's stated preference is
to add no long-term persistence. The prior art for this integration
(`educates-litellm-manager`) was forced into the API-client shape because LiteLLM
offers nothing else, and inherited three defects from it: the admin credential
persisted in plaintext in CR status so it could be re-read on delete; a
create-only reconcile with no update path; and revocation failures swallowed with
"manual cleanup may be required".

## Decision

Target agentgateway's Kubernetes mode. The operator mints a key by writing a
labelled ConfigMap; it makes no HTTP calls to agentgateway at all.

## Consequences

The operator is a plain controller-runtime reconciler over Kubernetes objects.
No HTTP client, no credential to hold for callbacks, no retry-on-401 readiness
dance, no database, no unauthenticated port to protect.

All three inherited defects are structurally impossible rather than merely
avoided. There is no admin credential to leak into status, because there is no
admin API. Reconcile is naturally idempotent because it converges objects rather
than issuing commands. Revocation is a ConfigMap delete, which either succeeds or
is retried by the same reconcile loop as everything else.

The cost is a hard dependency on agentgateway's Kubernetes mode and its CRDs
(`AgentgatewayPolicy`, `AgentgatewayBackend`), and on a Gateway API
implementation. Standalone mode's simpler single-pod deployment is given up.

Reversing this means rewriting the operator as an HTTP client and introducing a
database, effectively a different operator. Hence an ADR.

## Notes

Verified against agentgateway v1.5.x docs and the source at
`agentgateway/agentgateway@b0778552`.
