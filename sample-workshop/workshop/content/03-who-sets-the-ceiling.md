Your budget was not chosen by the workshop you are sitting in. The grant asked
for nothing, and a number arrived anyway. This page is where it came from.

## Two people, two objects

A workshop author writes the grant. A cluster operator owns the **catalog**,
and with it the provider credential that every request in this room is
ultimately billed to. Those are usually different people, and the split matters
because only one of them pays.

Look at the operator's half:

```execute
kubectl get agentgatewaycatalog cluster \
  -o jsonpath='{.spec.budgets}{"\n"}'
```

Two numbers. `defaultTokenBudget` is what a grant gets when it asks for
nothing, which is exactly what your grant did. `maxTokenBudget` is the most any
workshop may have, whatever it asks for.

## Why the default lives there

An author who writes `tokenBudget: 20000` into a workshop definition has frozen
a number into a file that is edited rarely and redeployed rarely. Six months
later the cluster has moved to a cheaper model, or a more expensive one, and
that number is still 20000 because nobody remembered it was there.

An author who writes nothing gets whatever the operator currently thinks is
reasonable. Changing it is a one-line edit to the catalog, and it reaches
sessions that are *already running*:

```
kubectl patch agentgatewaycatalog cluster --type=merge \
  -p '{"spec":{"budgets":{"defaultTokenBudget":40000}}}'
```

You cannot run that here, it needs cluster-operator rights, but the mechanism
is worth knowing because it explains something you can see. Your registration
carries no budget at all:

```execute
kubectl get agentgatewaysession "$SESSION_NAME" -n "$WORKSHOP_NAMESPACE" \
  -o jsonpath='{.status.effectiveTokenBudget}{"\n"}'
```

That number is on the grant's **status**, computed by the operator, and
nowhere else. It is not written onto your key. The gateway falls back to a
single shared limit for any key that does not carry its own, and that shared
limit is what the catalog renders. One object changes, every inheriting session
follows, and nothing has to be reconciled per attendee.

That is also why `status` is the only honest place to read a budget from, the
point page 3 made from the other direction.

## The ceiling

`maxTokenBudget` is a different kind of number. The default is a suggestion an
author can override; the maximum is not.

An author who asks for more than the operator allows does not get it, and does
not get an error either. The grant is **clamped**:

```
spec:
  tokenBudget: 500000     # more than maxTokenBudget

status:
  effectiveTokenBudget: 50000
  conditions:
    - type: BudgetWithinLimits
      status: "False"
      reason: BudgetClamped
      message: requested a token budget of 500000, clamped to the catalog maximum of 50000
```

Clamped rather than rejected, on purpose. Rejecting would fail every attendee's
session at start, and a workshop that does not run is worse than a workshop
that runs on a smaller budget. The grant still reaches `Ready`.

Your own grant is within the ceiling, so the condition reads the other way:

```execute
kubectl get agentgatewaysession "$SESSION_NAME" -n "$WORKSHOP_NAMESPACE" \
  -o jsonpath='{range .status.conditions[?(@.type=="BudgetWithinLimits")]}{.status}{": "}{.message}{"\n"}{end}'
```

Check that condition when a workshop behaves as though its budget were smaller
than the one it asked for. It usually is.

## How long a budget lasts

A budget is not a ceiling for your session's lifetime. It is a ceiling per
**window**, and the grant says which:

```execute
kubectl get agentgatewaysession "$SESSION_NAME" -n "$WORKSHOP_NAMESPACE" \
  -o jsonpath='{.spec.budgetWindow}{"\n"}'
```

A day, which outlasts any workshop, so in practice you get one budget for the
whole session. That is the reason for the default: with an hourly window a
two-hour workshop would quietly hand out two full budgets, which is not what
anyone writing `tokenBudget` believes they are configuring.

`budgetWindow` and `ttl` answer different questions and are deliberately
independent: how long a budget lasts, and how long the key works at all.

One honest caveat. Windows are aligned to the clock, not to when your session
started, so a daily window resets at midnight UTC. A workshop running across
midnight sees one reset. The guarantee is "at most one", and the TTL from
page 5 bounds it, since an expired key cannot spend the second budget.

## Budgets in dollars

Everything above counts tokens. Tokens are a decent proxy for cost until two
models have different prices, at which point the same ceiling means two
different bills.

A **cost budget** fixes that by charging each request what the provider
actually bills for it:

```
budgets:
  defaultCostBudget: "0.25"
  maxCostBudget: "1.00"
```

Dollars, written as a string rather than a number so that no rounding error can
creep into a value the gateway compares for equality. It resolves and clamps
exactly as the token budget does, and it is enforced *alongside* it rather than
instead of it: whichever runs out first stops you.

This cluster does not set one, which you can confirm:

```execute
kubectl get agentgatewaysession "$SESSION_NAME" -n "$WORKSHOP_NAMESPACE" \
  -o jsonpath='effectiveCostBudget: [{.status.effectiveCostBudget}]{"\n"}'
```

Empty, because pricing has to come from somewhere. agentgateway prices requests
against its own built-in cost catalog, and this workshop is configured against a
local model that reports no price at all, so a cost budget here would only ever
charge a flat fallback figure rather than a real one. Against a hosted provider
it is a genuine dollar ceiling.

The token budget stays either way, and not merely for backwards compatibility:
a cost expression can fail to evaluate, and when it does the gateway skips that
limit and says so only in a debug log. The token budget uses no expression, so
it cannot be skipped. It is the backstop.

Next: what happens to all of this when your session ends.
