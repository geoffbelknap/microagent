# Agent examples (Agentfiles)

These are **Agentfiles** — ordinary microagent workspace specs (`--file`) that use
the optional `agent:` block to describe an agent in a few lines of declarative
YAML. You ship the Agentfile plus your agent script; microagent assembles the rest
on boot. **There is no image to build and no fat image to ship** — the base is a
thin public image, the SDK is installed at boot, and your task credential is
injected host-side so the guest never holds it.

```bash
microagent dispatch --file examples/agents/openai-agent/agent.yaml
```

That one command: pulls the thin base → installs the SDK inside the booted microVM
→ drops your `agent.py` → runs it under the egress envelope, with the provider API
key swapped in at the edge. When it finishes you also get the mediator-written
**egress audit receipt** — a tamper-proof record of every host the agent reached.

## The `agent:` block

| field | meaning |
|---|---|
| `entry` | the command that runs your agent (the one-shot exec) |
| `egress` | `guarded` (default) · `strict` · `off` |
| `allow` | extra egress hosts to allowlist (unioned) |
| `cred-swap` | built-in providers to inject host-side: `anthropic`, `openai`, `gemini`, `groq`, `openrouter`, `deepseek` — each `PROVIDER[=env:NAME\|file:PATH\|vault:PATH]` |

Everything else (`image`, `setup`, `files`, `env`) is a normal workspace spec field.
CLI flags override the Agentfile (e.g. `--egress strict` beats `agent.egress`).

## How cred-swap works here (and its boundary)

`cred-swap: [anthropic]` allowlists `api.anthropic.com` and tells the mediator to
inject the real key into the provider's auth header **host-side**. The guest carries
only a worthless **placeholder** key (set in `env:` below) — the mediator *replaces*
the header at the edge, so the real key never enters the VM.

This protects the **task credential** the agent uses (the provider API key). It is
**not** leakproof and it does **not** protect your own machine's auth — it bounds
one blast radius (this key), and you choose the egress envelope around it. A
prompt-injected agent can't exfiltrate a key it never held.

The default `guarded` egress allows public destinations (so `pip install` and the
provider call both work) while denying the private/internal network and recording
everything in the audit receipt. For a tighter envelope, see the per-example
README notes on the `strict` + `commit` pattern.

> **Validation note.** These examples are turnkey *recipes*. A full agent turn
> needs a real `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` on the host (referenced by
> cred-swap). Verify the SDK snippet against the current SDK docs and run once with
> your key before relying on it. The `agent.py` scripts are intentionally minimal.
