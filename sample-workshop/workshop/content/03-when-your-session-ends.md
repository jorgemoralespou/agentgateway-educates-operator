When you close this session, your key stops working. Not "is scheduled for
cleanup", stops working, because both objects that make it valid are removed.

This is the one thing you cannot watch from inside the session, since the
session is what is being destroyed. So instead, here is the wiring that
guarantees it, visible right now.

## Half one: owned by your session namespace

Your Secret carries an owner reference:

```execute
kubectl get secret "$SESSION_NAME-agentgateway" -n "$WORKSHOP_NAMESPACE" \
  -o jsonpath='{.metadata.ownerReferences[0].kind}/{.metadata.ownerReferences[0].name}{"\n"}'
```

`Namespace/<your session namespace>`, not the workshop namespace it lives in.

That is deliberate, and it is what makes revocation automatic. Kubernetes
garbage collection deletes an object when its owner goes. Your session
namespace is deleted the moment your session ends, so the Secret holding your
plaintext key is collected by the cluster itself. No controller has to be
running, no cleanup job has to succeed.

The Secret sits in the workshop namespace (so your pod can read it) but is
owned from the session namespace (so it dies with your session). Two
namespaces, two different lifecycles, one object bridging them.

## Half two: removed by a finalizer

The registration in the gateway namespace, the hash from page 2, is not owned
by anything, so garbage collection will not touch it. A finalizer on the grant
handles that one:

```execute
kubectl get agentgatewaysession "$SESSION_NAME" -n "$WORKSHOP_NAMESPACE" \
  -o jsonpath='{.metadata.finalizers}{"\n"}'
```

The operator will not let the grant be deleted until it has removed the
registration from the gateway. If it cannot, it says so loudly rather than
silently leaving a live credential behind.

## And a backstop

Your grant also carries an expiry, from `ttl: 2h`:

```execute
kubectl get agentgatewaysession "$SESSION_NAME" -n "$WORKSHOP_NAMESPACE" \
  -o jsonpath='{.status.expiresAt}{"\n"}'
```

If a session is somehow never cleaned up: a cluster failure at the wrong
moment, a namespace stuck terminating: the key still stops working at that
time. Cleanup that depends on nothing going wrong is not cleanup.

## What an administrator would see

After this session ends, both lookups return `NotFound`:

```
kubectl get secret    "$SESSION_NAME-agentgateway" -n <workshop namespace>
kubectl get configmap "$SESSION_NAME-agentgateway" -n agentgateway-system
```

And a copy of your key saved beforehand returns `401`. The credential is not
merely unreachable: it no longer exists anywhere.

## What you saw

- One resource in a workshop definition gave every attendee their own key.
- The key was minted for your session, budgeted independently, and never
  written into any status field.
- It is revoked by the cluster's own garbage collector, with a finalizer for
  the half garbage collection cannot reach, and a TTL behind both.
