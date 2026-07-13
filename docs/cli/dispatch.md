---
title: microagent dispatch
description: Run one task in a fresh, isolated, single-use workspace and get back the result plus an egress audit receipt.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-13_

```text
microagent dispatch <image> [command arg...] [flags]
microagent dispatch --image <ref> --exec "<command>" [flags]
microagent dispatch --file <agent.yaml> [flags]
```

`dispatch` boots a throwaway microVM under the egress guardrails you choose, runs
one command, and returns its result **and** a summary of what it reached on the
network — the mediator-written audit — then tears the workspace down. It is the
one-call "delegate this to an isolated machine and tell me what it did" primitive:
ideal for handing untrusted, dangerous, or parallel work to its own machine.

It is one-shot: nothing persists. Use [`run`](/cli/run/) when you want the same
disposable boot but not the audit receipt, or [`create`](/cli/create/) when you
want a named workspace that survives.

## Why dispatch

The audit is what sets `dispatch` apart. Every dispatched task's
network traffic passes through a small host-side process — the mediator — and
it is the mediator, not the guest, that writes the record of every connection
attempt. Because that record lives outside the guest's control, a
prompt-injected or otherwise-rogue task can neither forge nor suppress it. Under the default `broker` mode the mediator records allowed
destinations too (not just denials), so the summary reflects real behavior.

Pair it with [credential swap](/concepts/egress-mediation/#credential-swap): the
guest can *use* a provider API key it can never read, because the real secret is
injected host-side at the mediator.

## Examples

Run a command and throw the VM away, keeping the audit receipt:

```bash
microagent dispatch docker.io/library/python:3.12 python -c 'print(2+2)'
```

Lock the task down to a handful of hosts — everything else is denied and shows
up in the audit as a denial:

```bash
microagent dispatch --egress broker --egress-lock-allowlist \
  --egress-allow api.example.com \
  docker.io/library/python:3.12 python agent.py
```

Delegate work that uses a provider key the guest never holds
([credential swap](/concepts/egress-mediation/#credential-swap) requires
`--egress mitm`):

```bash
microagent dispatch --egress mitm --cred-swap anthropic \
  docker.io/library/python:3.12 python agent.py
```

Run an [Agentfile](/cli/spec/#agentfile-the-agent-block) — a build-free agent
recipe — in one call:

```bash
microagent dispatch --file examples/agents/openai-agent/agent.yaml
```

With `--json` the result and audit are machine-readable:

```json
{
  "workspace": "dispatch-swift-falcon-9k4t",
  "final_state": "stopped",
  "result": { "exit_code": 0, "stdout": "4\n" },
  "audit": { "decision_count": 3, "allow_by_host": { "example.com": 1 } }
}
```

## Flags

`dispatch` shares the workspace flagset with [`run`](/cli/run/); the most relevant:

| Flag | Description |
|---|---|
| `--image <ref>` | OCI image to boot (or the first positional argument) |
| `--exec <command>` | Command to run (alternative to the positional `command`) |
| `--file <path>` | Workspace spec / [Agentfile](/cli/spec/); flags override matching spec fields |
| `--network <mode>` | Network mode: `user` (default) or `isolated` |
| `--timeout <seconds>` | Maximum wall-clock time before the task is killed |
| `--egress <mode>` | [Egress mediation](/concepts/egress-mediation/) mode: `broker` (default), `mitm`, or `off` |
| `--egress-lock-allowlist` | Only allowlisted hosts are reachable. Works in `broker` or `mitm` |
| `--egress-allow <host>` | Allowlisted egress destination. Repeatable; an exact host or a `.suffix` matching the apex and subdomains |
| `--egress-passthrough <host>` | Allowed egress destination that is **not** TLS-intercepted. Repeatable. For cert-pinned / mTLS endpoints |
| `--egress-policy <path>` | Egress policy file (`.yaml`/`.yml`/`.json`) declaring `allow[]` / `passthrough[]`; unioned with the flags. Requires `--egress broker` or `mitm` |
| `--egress-swap-config <path>` | Credential-swap config (YAML): the mediator injects the real credential host-side so the guest never holds it. Requires `--egress mitm`. See [credential swap](/concepts/egress-mediation/#credential-swap) |
| `--cred-swap PROVIDER[=ref]` | Credential swap for a built-in provider (`anthropic`, `openai`, `gemini`, `groq`, `openrouter`, `deepseek`); the optional `=ref` is a reference, never a literal secret. Repeatable; requires `--egress mitm` |
| `--secret NAME=<scheme>:<ref>` | Deliver a secret to the guest tmpfs `/run/secrets`. Repeatable. See [`secret`](/cli/secret/) |
| `--secret-on-demand NAME=<scheme>:<ref>` | Declare an on-demand secret fetched at runtime, never written to tmpfs. Repeatable |
| `--secrets-env-file <path>` | Deliver every key in a dotenv file as a secret |
| `--secrets-audit` | Append every secret access to the workspace audit log |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--supervisor`. The full
shared flag reference (resources, model pairing, storage, networking) is documented under [`run`](/cli/run/).

## Related

- [`run`](/cli/run/) - the disposable one-shot boot without the audit receipt
- [`spec`](/cli/spec/) - the workspace spec / Agentfile format `--file` accepts
- [`egress`](/cli/egress/) - read a workspace's recorded egress audit decisions
- [credential swap](/concepts/egress-mediation/#credential-swap) - inject a key the guest never holds
