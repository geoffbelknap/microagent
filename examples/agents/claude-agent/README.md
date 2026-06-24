# Claude Agent SDK agent

A [Claude Agent SDK](https://docs.claude.com/en/api/agent-sdk/overview) agent in
an isolated microVM, defined by an Agentfile — no image to build.

## Run it

```bash
export ANTHROPIC_API_KEY=sk-ant-...      # your real key (host-side, referenced by cred-swap)
microagent dispatch --file examples/agents/claude-agent/agent.yaml
```

What happens: pull `python:3.12-slim` → `pip install claude-agent-sdk` inside the
booted VM → drop `agent.py` → run it under **guarded** egress. The SDK sends
`x-api-key: <placeholder>` to `api.anthropic.com`; the mediator **replaces** it
host-side with your real `ANTHROPIC_API_KEY`. The guest never holds the real key,
and the egress audit receipt records everything the agent reached.

## Caveats specific to this SDK

- **Bundled CLI / Node.** `claude-agent-sdk` bundles the Claude Code CLI and shells
  out to it. The pip package is meant to be self-contained, but if your base needs
  Node.js for the bundled CLI to run, add it to `setup` (e.g.
  `apt-get update && apt-get install -y nodejs`) or start from a Node-capable base.
- **Egress footprint.** Claude Code may reach hosts beyond `api.anthropic.com`
  (e.g. telemetry). Under the default `guarded` mode that's allowed and shows up in
  the audit receipt — a good way to *see* its real footprint. Under `strict` you'd
  need to allowlist those hosts too (start from the audit receipt of a guarded run).
- **Subscription vs API key.** cred-swap protects an **API key**. The Claude Agent
  SDK here authenticates with `ANTHROPIC_API_KEY`; it is not a way to put a
  subscription/OAuth session into the box.

## Notes

- `agent.py` is intentionally minimal — adapt it and verify the SDK API against the
  current docs.
- A full agent turn requires a real `ANTHROPIC_API_KEY`; validate once before
  relying on the example.
