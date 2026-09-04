# End-to-end validation against a live Educates workshop

Ticket 09. **Not part of CI**: this needs a live Educates cluster with a
training portal, and is run deliberately before a release.

What it proves is the thing no automated test here can: that a workshop author's
whole integration surface is one resource plus two environment variables, and
that an attendee's session starts with a working LLM key that stops working when
they finish.

## Why this is not automated

Every layer below it is. The reconcilers are covered at the envtest seam, and
the cross-namespace garbage collection ADR-0002 rests on is covered by
`make test-e2e` on kind. What remains is the Educates platform contract itself,
session Deployments landing in the workshop namespace, session objects owned by
the session namespace, and standing up a live Educates cluster with a portal in
CI would cost far more than it catches.

That contract was validated by hand on a live v4 cluster on 2026-09-03 and
recorded in ADR-0002. This runbook re-checks it against a real workshop.

## Prerequisites

- An Educates v4 cluster.
- This operator installed, with a ready `AgentGatewayPlatform` and
  `AgentGatewayCatalog`:

  ```console
  kubectl get agentgatewayplatform cluster
  kubectl get agentgatewaycatalog cluster
  ```

  Both must report `Ready`. The catalog's `Models` column lists the names a
  workshop may reference.

- A **TrainingPortal**. This is easy to miss: sessions are allocated through a
  portal, and a `WorkshopEnvironment` that is not registered with one produces
  no sessions at all. If nothing happens when you request a session, check this
  first.

## The workshop definition

The whole integration surface. Note `namespace: $(workshop_namespace)` on the
session grant: it is load-bearing and easy to omit.

```yaml
apiVersion: training.educates.dev/v1beta1
kind: Workshop
metadata:
  name: llm-validation
spec:
  title: LLM access validation
  description: Confirms an attendee gets a working, budgeted LLM key.
  workshop:
    files:
      - image:
          url: $(image_repository)/workshop-content:latest
  session:
    namespaces:
      budget: small

    env:
      # The two environment variables. Standard OpenAI-compatible client
      # libraries pick both of these up with no configuration.
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

    objects:
      - apiVersion: agentgateway.operators.educates.dev/v1alpha1
        kind: AgentGatewaySession
        metadata:
          name: $(session_name)
          # REQUIRED. Without it the resource lands in the session namespace,
          # the Secret follows it there, and the attendee's pod cannot resolve
          # the secretKeyRef above: it wedges in CreateContainerConfigError
          # with no useful diagnostic (ADR-0002).
          #
          # The operator rejects a grant in a session namespace with an
          # explanatory condition rather than letting that happen, so if you
          # omit this you will get a clear error rather than a stuck pod.
          namespace: $(workshop_namespace)
        spec:
          catalogRef:
            name: cluster
          tokenBudget: 100000
          ttl: 4h
```

## What to check

### 1. The attendee's session starts with a working key

Request a session through the portal, then in the attendee's terminal:

```console
echo "$OPENAI_BASE_URL"
# http://agentgateway-educates.agentgateway-system.svc.cluster.local:4000

echo "${OPENAI_API_KEY:0:6}..."
# sk-...   (never print the whole key)
```

Reach the LLM by catalog name, never by the provider's model name, which the
attendee should not know:

```console
curl -sS "$OPENAI_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model": "fast", "messages": [{"role": "user", "content": "say hi"}]}'
```

A 200 with a completion. A 401 means the registration is not being matched by
the policy; a 404 on the model means the catalog name is wrong.

### 2. Both halves exist, in their two namespaces

From a cluster-admin context, for a session `$SESSION` in workshop environment
`$WORKSHOP`:

```console
# The plaintext, in the workshop namespace where the attendee's pod can read it.
kubectl get secret "$SESSION-agentgateway" -n "$WORKSHOP"

# Owned by the SESSION namespace, not the WorkshopSession. This is the
# ADR-0002 arrangement, and the published Educates docs say otherwise.
kubectl get secret "$SESSION-agentgateway" -n "$WORKSHOP" \
  -o jsonpath='{.metadata.ownerReferences[0].kind}/{.metadata.ownerReferences[0].name}{"\n"}'
# Namespace/<session namespace>

# Only the hash, in the gateway namespace.
kubectl get configmap "$SESSION-agentgateway" -n agentgateway-system -o yaml
# data holds {"keyHash":"sha256:...","metadata":{"session":"..."}} and no key.
```

Also confirm the attendee's Deployment really is in the workshop namespace,
which is the assumption the whole design rests on:

```console
kubectl get deployment -n "$WORKSHOP"
kubectl get deployment -n "$SESSION"   # expect: no resources found
```

### 3. A budget exhausts for one attendee only

Start two sessions. In the first, burn the budget: a loop of large-context
requests is quickest:

```console
for i in $(seq 1 200); do
  curl -sS -o /dev/null -w '%{http_code} ' "$OPENAI_BASE_URL/v1/chat/completions" \
    -H "Authorization: Bearer $OPENAI_API_KEY" \
    -H 'Content-Type: application/json' \
    -d '{"model": "fast", "messages": [{"role": "user", "content": "'"$(head -c 2000 /dev/urandom | base64)"'"}]}'
done
```

Expect a run of 200s then 429s. Two upstream behaviours to expect, both
documented and neither a bug here:

- the request that *crosses* the limit completes; only the next one is rejected
- rate limiting runs *before* prompt guards, so a guardrail-rejected request
  still consumes budget

Then, in the **second** session, make one request. It must still return 200.
That is the whole point: one attendee's runaway loop yields 429s for that
attendee alone.

### 4. Ending the session revokes the key

Delete the session through the portal, or:

```console
kubectl delete workshopsession "$SESSION"
```

Then:

```console
# Collected with the session namespace.
kubectl get secret "$SESSION-agentgateway" -n "$WORKSHOP"
# Error from server (NotFound)

# Removed by the finalizer.
kubectl get configmap "$SESSION-agentgateway" -n agentgateway-system
# Error from server (NotFound)
```

And the key itself is dead, reusing a copy saved from step 1 must now return
401:

```console
curl -sS -o /dev/null -w '%{http_code}\n' "$OPENAI_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $SAVED_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model": "fast", "messages": [{"role": "user", "content": "hi"}]}'
# 401
```

### 5. The wrong-namespace error is actually helpful

Worth checking once, because it is the mistake an author will make. Remove
`namespace: $(workshop_namespace)` from the grant, redeploy, and request a
session:

```console
kubectl get agentgatewaysession -n "$SESSION" -o yaml
```

The `PlacementValid` condition must be `False` with a message naming
`$(workshop_namespace)`, and **no** Secret or registration may have been
created. The attendee's pod should fail visibly rather than sitting in
`CreateContainerConfigError`.

## Recovering leaked registrations

If a cleanup gave up, the operator logs loudly when it does, find and remove
what it left:

```console
# Everything this operator registered.
kubectl get configmap -n agentgateway-system -l agentgateway.dev/apikey=true

# One session's registration.
kubectl delete configmap -n agentgateway-system \
  -l agentgateway.operators.educates.dev/session="$SESSION"
```

A leaked registration is a stale hash, not a credential: the plaintext died with
the workshop namespace, and the key's TTL closes even that window.
