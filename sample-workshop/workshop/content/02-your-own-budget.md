Your key carries a token budget, `tokenBudget: 20000` in the grant on the last
page. It is deliberately small here so you can hit it inside a workshop
session. A real workshop would set it to whatever its exercises need.

The point of a per-attendee budget is not to stop you working. It is that a
runaway loop, an accidental infinite retry, or one person pasting a novel into
a prompt cannot exhaust the shared account and take the workshop down for
everyone else.

## Spend it

Each request below sends a large prompt, so the budget goes quickly. Watch the
status codes:

```execute
for i in $(seq 1 40); do
  curl -sS -o /dev/null -w '%{http_code} ' "$OPENAI_BASE_URL/v1/chat/completions" \
    -H "Authorization: Bearer $OPENAI_API_KEY" \
    -H 'Content-Type: application/json' \
    -d '{"model": "fast", "messages": [{"role": "user", "content": "'"$(head -c 3000 /dev/urandom | base64 | tr -d '\n')"'"}]}'
done
echo
```

A run of `200`s, then `429`s. That is your budget being consumed and then
refused.

Two behaviours here are worth knowing, because both look like bugs and neither
is:

- **The request that crosses the limit still completes.** Accounting happens
  after the fact, so the limit is enforced on the *next* request, not the one
  that exceeded it.
- **Rate limiting runs before prompt guards.** A request that a guardrail would
  have rejected still consumes budget. Being refused for content is not a free
  retry.

## It is yours alone

This is the part that matters. Your neighbour's key is unaffected, their
budget is separate, tracked against their own key hash.

If someone next to you still has budget, ask them to run this in *their*
terminal while yours is returning 429:

```
curl -sS -o /dev/null -w '%{http_code}\n' "$OPENAI_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model": "fast", "messages": [{"role": "user", "content": "hi"}]}'
```

They get a `200`. Same gateway, same upstream account, same model, different
key, different budget.

Working alone? Request a second session from the training portal in another
browser tab and run it there.

## Checking what you have left

The grant's status carries the session's phase and expiry:

```execute
kubectl get agentgatewaysession "$SESSION_NAME" -n "$WORKSHOP_NAMESPACE" \
  -o jsonpath='{.status.phase}{"\n"}{.status.expiresAt}{"\n"}'
```

The budget itself is not counted down in status: the gateway tracks
consumption against your key hash, and the grant only records what you were
allotted:

```execute
kubectl get agentgatewaysession "$SESSION_NAME" -n "$WORKSHOP_NAMESPACE" \
  -o jsonpath='{.spec.tokenBudget}{"\n"}'
```

Next: what happens to this key when you finish.
