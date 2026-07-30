# OpenAI Agents SDK agent

A turnkey [OpenAI Agents SDK](https://openai.github.io/openai-agents-python/)
agent in an isolated microVM, defined by an Agentfile — no image to build.

## Run it

```bash
# The real key lives only on your host; the guest gets a reference.
export OPENAI_API_KEY=sk-...
microagent dispatch --file examples/agents/openai-agent/agent.yaml
```

What happens: pull `python:3.12-slim` → `pip install openai-agents` inside the
booted VM → drop `agent.py` → run it against a **broker endpoint**. The guest's
`OPENAI_API_KEY` is the literal reference `@secret:openai` and
`OPENAI_BASE_URL` points at the broker's in-guest address; the broker swaps the
reference for your real key host-side (resolved from `env:OPENAI_API_KEY`) and
originates its own TLS to `api.openai.com`. The guest never holds the real key,
no CA is installed in the guest, and every brokered request lands in the
decision stream (`microagent egress <name>`).

## Tightening the envelope (locked allowlist)

Brokered provider traffic rides a vsock channel, not the guest's network
device — so you can lock the NIC path down completely and the agent still
reaches its provider. Pre-bake the deps so no boot-time install is needed,
then lock the allowlist with nothing on it:

```bash
# 1. Build a reusable image once (deps baked in), via commit:
microagent run --name openai-base --image docker.io/library/python:3.12-slim \
  --setup "pip install --no-cache-dir openai-agents" --keep --exec true
microagent commit openai-base local/openai-agent-base:latest
# 2. Dispatch the baked image with the NIC allowlist locked; the broker
#    endpoint keeps working because it does not ride the NIC:
microagent dispatch --image local/openai-agent-base:latest \
  --egress-lock-allowlist \
  --file examples/agents/openai-agent/agent.yaml
```

The result: ordinary network egress is allowlist-only (here: nothing), while
the brokered `api.openai.com` path — with its per-request decision records —
is the agent's only way out.

## Notes

- `agent.py` is intentionally minimal — adapt it to your task. Verify the SDK API
  against the current docs.
- A full agent turn requires a real `OPENAI_API_KEY`; validate once before relying
  on the example.
