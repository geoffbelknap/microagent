# OpenAI Agents SDK agent

A turnkey [OpenAI Agents SDK](https://openai.github.io/openai-agents-python/)
agent in an isolated microVM, defined by an Agentfile — no image to build.

## Run it

```bash
# The real key lives only on your host; cred-swap injects it at the edge.
export OPENAI_API_KEY=sk-...            # your real key (host-side, referenced by cred-swap)
microagent dispatch --file examples/agents/openai-agent/agent.yaml
```

What happens: pull `python:3.12-slim` → `pip install openai-agents` inside the
booted VM → drop `agent.py` → run it under **mitm** egress (credential swap
rewrites the auth header inside TLS, so it needs interception). The SDK sends
`Authorization: Bearer <placeholder>` to `api.openai.com`; the mediator **replaces**
it host-side with your real `OPENAI_API_KEY` (resolved from `env:OPENAI_API_KEY` on
the host). The guest never holds the real key. You also get the egress audit
receipt showing exactly what the agent reached.

## Tightening the envelope (locked allowlist)

The mediating modes allow public egress by default, so both the boot-time
`pip install` and the provider call work. To confine the run to allowlisted
hosts only, pre-bake the deps so no install is needed at run time, then lock
the allowlist:

```bash
# 1. Build a reusable image once (deps baked in), via commit:
microagent run --name openai-base --image docker.io/library/python:3.12-slim \
  --setup "pip install --no-cache-dir openai-agents" --keep --exec true
microagent commit openai-base openai-agent-base:latest
# 2. Dispatch the baked image with the allowlist locked; cred-swap needs mitm
#    and auto-allows api.openai.com:
microagent dispatch --image openai-agent-base:latest \
  --egress mitm --egress-lock-allowlist \
  --cred-swap openai --file - <<'EOF'   # or inline flags
EOF
```

(With `--egress-lock-allowlist`, only `api.openai.com` — auto-allowlisted by
cred-swap — is reachable; the boot-time install is skipped because the deps are
already baked.)

## Notes

- `agent.py` is intentionally minimal — adapt it to your task. Verify the SDK API
  against the current docs.
- A full agent turn requires a real `OPENAI_API_KEY`; validate once before relying
  on the example.
