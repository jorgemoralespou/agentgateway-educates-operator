# 4. Generate participant keys in the operator and register only their hashes

Date: 2026-09-03

## Status

Accepted

## Context

In agentgateway's Kubernetes mode there is no minting API, nothing upstream
issues a credential. Whatever appears in a key registration is whatever the
author of that object put there. So the operator generates the key itself.

This is a real departure from the prior art, where LiteLLM's `POST /user/new`
returned a key with server-side identity, a budget and a model allowlist attached.
Here the key is a random string with no existence outside the two objects this
operator writes. The glossary records this as **participant key** rather than
"virtual key" precisely because the two are not the same kind of thing.

agentgateway accepts either a raw `key` or a `keyHash` of the form
`sha256:<hex>`, computed over the exact key bytes with no canonicalisation. In a
**ConfigMap** a raw key is rejected outright: the controller errors with "keys
sourced from a ConfigMap must use keyHash, not a raw key, since ConfigMaps are
not confidential" (`plugins/traffic_plugin.go:983`).

## Decision

The operator generates a cryptographically random, `sk-`-prefixed participant key
per session. The plaintext goes only into the attendee's Secret. Only
`sha256:<hex>` is registered with the Gateway, in a ConfigMap.

## Consequences

The key is known the instant it is generated: no propagation race, no
retry-on-401, no window where the Secret exists but the key is not yet valid.

The gateway-side record holds no credential material, so the registration
ConfigMap is not a secret and is honestly typed as a ConfigMap rather than a
Secret. This is what makes the leak in ADR-0002 tolerable.

The plaintext is unrecoverable once written. If the Secret is lost, the key
cannot be recovered: a new one is generated and the registration updated. This
makes rotation the recovery path, and means reconcile must generate a key only
when the Secret is genuinely absent, never on every pass.

Nothing upstream knows the key exists. There is no external console listing
issued keys, no server-side revocation independent of Kubernetes, and no
per-key usage record beyond what the Gateway's own metrics and access logs carry.

Metadata on the registration is arbitrary JSON, flattened onto the CEL `apiKey`
object, `apiKey.session`, not `apiKey.metadata.session`
(`http/apikey.rs:53-60`). The field name `key` is reserved for the redacted key
itself and must not be used for metadata.

## Notes

`keyHash` is over raw key bytes with no trailing newline. Hex is normalised to
lowercase on input. Verified in both control plane (`validateAPIKeyHash`,
`traffic_plugin.go:888`) and data plane (`apikey.rs:119-132`).
