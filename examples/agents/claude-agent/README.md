# Claude Agent SDK agent

A [Claude Agent SDK](https://docs.claude.com/en/api/agent-sdk/overview) agent in
an isolated microVM, defined by an Agentfile — no image to build.

## Run it

```bash
export ANTHROPIC_API_KEY=sk-ant-...      # your real key (host-side; the guest gets a reference)
microagent dispatch --file examples/agents/claude-agent/agent.yaml
```

What happens: pull `python:3.12-slim` → `pip install claude-agent-sdk` inside
the booted VM → drop `agent.py` → run it against a **broker endpoint**. The
guest's `ANTHROPIC_API_KEY` is the literal reference `@secret:anthropic` and
`ANTHROPIC_BASE_URL` points at the broker's in-guest address; the broker swaps
the reference for your real key host-side and originates its own TLS to
`api.anthropic.com`. The guest never holds the real key, no CA is installed in
the guest, and every brokered request lands in the decision stream
(`microagent egress <name>` shows `broker_request_allow` records — reference
names, never values).

## Caveats specific to this SDK

- **Bundled CLI / Node.** `claude-agent-sdk` bundles the Claude Code CLI and shells
  out to it. The pip package is meant to be self-contained, but if your base needs
  Node.js for the bundled CLI to run, add it to `setup` (e.g.
  `apt-get update && apt-get install -y nodejs`) or start from a Node-capable base.
- **Egress footprint.** Claude Code may reach hosts beyond the broker (e.g.
  telemetry). That traffic rides the ordinary mediated NIC path — allowed to
  public destinations by default and recorded in the same audit view, a good
  way to *see* the real footprint. With `--egress-lock-allowlist` the NIC path
  is confined to allowlisted hosts while the brokered provider traffic keeps
  working (it rides vsock, not the NIC).
- **Subscription vs API key.** The broker protects an **API key**. The Claude
  Agent SDK here authenticates with `ANTHROPIC_API_KEY`; it is not a way to put
  a subscription/OAuth session into the box.

## Notes

- `agent.py` is intentionally minimal — adapt it and verify the SDK API against the
  current docs.
- A full agent turn requires a real `ANTHROPIC_API_KEY`; validate once before
  relying on the example.
