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
→ drops your `agent.py` → runs it with a **broker endpoint** serving the provider
API, the key injected at the edge. When it finishes you also get the egress
audit view — a tamper-evident record of every host the agent reached and every
brokered request it made.

## The `agent:` block

| field | meaning |
|---|---|
| `entry` | the command that runs your agent (the one-shot exec) |
| `egress` | `broker` (default) · `mitm` · `off` |
| `allow` | extra egress hosts to allowlist (unioned) |
| `broker` | one broker endpoint: `upstream`, `secret` (`NAME=<scheme>:<ref>`), `env` (base-URL env keys), plus optional `proxy`/`capture`/`ca` |
| `brokers` | multiple endpoints, one block each — per-endpoint upstream, credential, and base-URL env |

Everything else (`image`, `setup`, `files`, `env`) is a normal workspace spec field.
A CLI `--broker-*`/`--broker-endpoint` flag wins outright; `agent.broker` fills an
otherwise-unset broker.

## How the broker endpoint works (and its boundary)

The endpoint's `secret` names a credential by **reference**
(`anthropic=env:ANTHROPIC_API_KEY`) that is resolved and held host-side only.
The guest gets two things: a base-URL env var pointing the SDK at the broker's
in-guest address, and an env credential that is the literal reference
`@secret:<name>`. The broker terminates the guest's plain-HTTP request on the
host, swaps the reference for the live value, and originates its **own TLS**
to the upstream — no certificate forging, no CA installed in the guest, and
cert-pinning clients are unaffected. Each request is recorded in the broker
decision stream (verdict, method, host, status, byte counts, and the
credential's *name*, never its value), merged into `microagent egress`.

This protects the **task credential** the agent uses. It is not leakproof and
it does not protect your own machine's auth — it bounds one blast radius (this
key), and you choose the egress envelope around it. A prompt-injected agent
can't exfiltrate a key it never held.

Ordinary network egress (the SDK install, telemetry, anything the agent fetches)
rides the mediated NIC path — public destinations allowed by default, the
private/internal network denied, everything recorded. For an allowlist-only
envelope, pre-bake the deps and add `--egress-lock-allowlist` — the brokered
provider traffic rides vsock, not the NIC, so it keeps working with the NIC
fully locked; see the per-example README notes.

For TLS *interception* (content inspection of non-brokered traffic), the
warned, opt-in `mitm` mode with `cred-swap` still exists — see
[egress mediation](../../docs/concepts/egress-mediation.md).

> **Validation note.** These examples are turnkey *recipes*. A full agent turn
> needs a real `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` on the host (referenced by
> the broker `secret`). Verify the SDK snippet against the current SDK docs and
> run once with your key before relying on it. The `agent.py` scripts are
> intentionally minimal.
