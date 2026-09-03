Your session already has a working LLM API key. Nothing to install, nothing to
sign up for, no key to paste. Two environment variables are set for you:

```execute
echo "$OPENAI_BASE_URL"
```

That is the gateway, running inside this cluster. Every request you make goes
through it.

```execute
echo "${OPENAI_API_KEY:0:6}..."
```

Your key — printed truncated, because it is a real credential. It is yours
alone: nobody else in this workshop has it, and it will stop working when your
session ends.

## Make a request

Ask for the model by its **catalog name**, not by a provider's model name:

```execute
curl -sS "$OPENAI_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model": "fast", "messages": [{"role": "user", "content": "Say hello in exactly five words."}]}'
```

A JSON completion comes back. Two names are available here — `fast` and
`smart` — and which upstream model each resolves to is the cluster operator's
decision, not yours. Try the other one:

```execute
curl -sS "$OPENAI_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model": "smart", "messages": [{"role": "user", "content": "Say hello in exactly five words."}]}' \
  | head -c 400
```

## Any OpenAI client works

`OPENAI_BASE_URL` and `OPENAI_API_KEY` are the two variables every
OpenAI-compatible library reads by default, so an SDK needs no configuration at
all:

```execute
python3 -c "
import os, json, urllib.request
req = urllib.request.Request(
    os.environ['OPENAI_BASE_URL'] + '/v1/chat/completions',
    data=json.dumps({'model': 'fast', 'messages': [{'role': 'user', 'content': 'Name one prime number.'}]}).encode(),
    headers={'Authorization': 'Bearer ' + os.environ['OPENAI_API_KEY'], 'Content-Type': 'application/json'},
)
print(json.load(urllib.request.urlopen(req))['choices'][0]['message']['content'])
"
```

If that returned a prime number, your session has everything it needs.

Two errors worth recognising if you go off-script: a **401** means the key is
not being accepted, and a **404** on the model means you asked for a name that
is not in the catalog.

Next: where this key came from, and what created it.
