# 8. Enable the AgentgatewayModel API, and opt the listener in to the kind

Date: 2026-09-04

## Status

Accepted

## Context

Every LLM request through the gateway returned `404`:

```
http.path=/v1/chat/completions http.status=404 error="route not found" reason=NotFound
```

Nothing else looked wrong. The platform was `Ready` with every condition
`True`, the catalog was `Ready` with `ModelsRendered=True`, the Gateway listener
was `Accepted` and `Programmed` with `ResolvedRefs=True`, and both
`AgentgatewayModel` objects existed with correct `parentRefs`. The only
symptom in the whole cluster was `attachedRoutes: 0` on the listener, and an
empty `status` on each model.

The empty status is a dead end by design: agentgateway v1.5.0 does not write
status back to `AgentgatewayModel` at all, so a model that failed to attach
looks exactly like one serving traffic. There is no condition anywhere that
reports this failure.

Two independent causes, both defaults.

### The API ships disabled

`AgentgatewayModel` is experimental in v1.5.0. The chart gates it behind
`agentgatewayModels.enabled`, defaulting to `false`, which becomes
`AGW_ENABLE_AGENTGATEWAY_MODELS` on the controller. With it off, the controller
ignores every model.

This operator's entire model story is that API, `AgentGatewayCatalog` renders
`AgentgatewayModel` pairs and nothing else, so the default is not a
configuration choice for us. It is the difference between a working platform
and one that 404s.

### The kind is opt-in per listener

Even with the API on, a listener accepts models only if it names the kind.
agentgateway's `GenerateSupportedKinds` gives an HTTP listener `HTTPRoute` and
`GRPCRoute` by default and appends `AgentgatewayModel` only when the listener's
`allowedRoutes.kinds` lists it explicitly. `allowedRoutes.namespaces.from: All`
does not imply it, namespaces and kinds are separate axes.

A model attaching to a listener that does not name its kind is dropped in
silence.

## Decision

**Set `agentgatewayModels.enabled: true`** in the control-plane chart values the
operator renders. Not a knob on `AgentGatewayPlatform`: a platform with the API
off has no working LLM path at all, so exposing it would only offer a way to
build a broken cluster.

**Name `AgentgatewayModel` in the listener's `allowedRoutes.kinds`**, alongside
`HTTPRoute`. Setting `kinds` narrows a listener to exactly what is listed, so
the `HTTPRoute` default has to be carried explicitly or it is revoked: a trap
for whoever adds the first route later, even though this operator creates none.

**Converge the listener on an existing Gateway.** The Gateway's spec is
otherwise written once at create, but a Gateway from an earlier build has a
listener that ignores every model rendered against it and nothing about that
self-corrects.

No `HTTPRoute` is created. Models attach directly to the listener and
agentgateway derives LLM routing from them, which is why the provider docs'
`HTTPRoute` + `AgentgatewayBackend` pattern does not apply here. An HTTPRoute
parent is a different, optional mode for scoping models under a path prefix,
which this operator does not use.

## Consequences

A cluster running an earlier build converges on the next reconcile: the
listener gains the kind, and the control-plane release picks up the flag on the
next Helm converge.

The chart does not ship the `httproutes` CRD, and naming the kind in
`allowedRoutes` does not require it, verified by deleting the CRD, restarting
the control plane, and confirming `attachedRoutes` stayed at 2 and requests
still routed. Listing a kind whose CRD is absent is inert.

Tracking an experimental API means this can change under us. The values
renderer and `listenerRouteKinds` are the two places to look if models stop
attaching after an agentgateway upgrade, and the check below is the fastest way
to tell.

## Notes

The diagnostic worth keeping, since no condition reports this failure:

```sh
# Is the API on?
kubectl get deploy agentgateway -n agentgateway-system \
  -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="AGW_ENABLE_AGENTGATEWAY_MODELS")].value}'

# Does the listener accept the kind? Must list AgentgatewayModel.
kubectl get gateway agentgateway-educates -n agentgateway-system \
  -o jsonpath='{.status.listeners[0].supportedKinds}'

# Are models actually attached? 0 means they are being dropped.
kubectl get gateway agentgateway-educates -n agentgateway-system \
  -o jsonpath='{.status.listeners[0].attachedRoutes}'
```

`attachedRoutes` counts `Internal` models too, so a catalog of one model shows
2. A `401` from `/v1/models` means routing works and the API-key policy is
doing its job; a `404` means it does not.
