Your key was created when your session started, by an operator watching for a
single custom resource. This page shows both halves of that: what the workshop
author wrote, and what the operator did with it.

## What the author wrote

The whole integration surface is one resource in the workshop definition:

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
        tokenBudget: 20000
        ttl: 2h
```

Plus two environment variables reading the Secret it produces:

```yaml
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

That is all. No key management, no per-attendee provisioning, no cleanup code.

`namespace: $(workshop_namespace)` is easy to overlook and load-bearing. Your
pod runs in the workshop namespace, and a `secretKeyRef` only resolves within
its own namespace, so a Secret created anywhere else would be invisible to
you. Omit that line and the operator refuses the grant with a `PlacementValid`
condition explaining why, rather than leaving you with a pod stuck in
`CreateContainerConfigError`.

## What the operator created

Look at the grant for your own session. It lives in the workshop namespace,
not the session namespace your terminal defaults to, so the commands here name
it explicitly:

```execute
kubectl get agentgatewaysession "$SESSION_NAME" -n "$WORKSHOP_NAMESPACE"
```

Its status says which phase it reached and where it put the Secret:

```execute
kubectl get agentgatewaysession "$SESSION_NAME" -n "$WORKSHOP_NAMESPACE" \
  -o jsonpath='{.status.phase}{"\n"}{.status.secretRef.name}{"\n"}{.status.gatewayURL}{"\n"}'
```

And the Secret it produced: the keys, not the values:

```execute
kubectl get secret "$SESSION_NAME-agentgateway" -n "$WORKSHOP_NAMESPACE" \
  -o go-template='{{range $k, $v := .data}}{{$k}}{{"\n"}}{{end}}'
```

Two keys: `api-key` and `base-url`, exactly the two environment variables you
used on the previous page.

## The half you cannot see

Your key exists in two places, and only one of them holds the key itself.

The Secret above has the plaintext, because your pod has to send it. In the
gateway's own namespace there is a second object, and it holds **only a hash**:

```
data:
  registration: {"keyHash":"sha256:...","metadata":{"session":"..."}}
```

The gateway authenticates you by hashing what you send and comparing. Nothing
on the gateway side can reproduce your key, which is why that object is an
ordinary ConfigMap rather than a Secret, and why it is safe for it to outlive
the moment of creation.

Notice too that the key never appears in the grant's `status`. A status field
is world-readable to anyone who can list the resource; a credential does not
belong there.

Next: the budget attached to that key.
