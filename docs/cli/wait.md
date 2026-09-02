---
title: microagent wait
description: Block until a workspace's run finishes, with the exit code reporting how it ended.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-15_

```text
microagent wait <name> [--timeout <dur>] [--state-dir <dir>]
```

`wait` blocks until the workspace reaches a terminal state - `stopped`,
`halted`, `failed`, `quarantined`, or `prepared` (created but never
started) - then prints that state and exits. The exit code says how the run
ended, so scripts follow a detached [`start`](start.md) without polling
[`status`](status.md) in a loop.

On a terminal, `wait` keeps one elapsed line current and reports the latest
observed workspace state. Redirected text records state changes without ANSI
control bytes. JSON and MCP output contain only the final typed result.

## Examples

Start an agent workspace and read its result as soon as the run finishes:

```bash
microagent start minimal-agent
microagent wait minimal-agent
microagent --json result minimal-agent
```

Or let `start` do the waiting in one step:

```bash
microagent start minimal-agent --wait
```

Give up if the run takes longer than five minutes:

```bash
microagent wait minimal-agent --timeout 5m
```

The structured output from `--json` and the MCP `workspace.wait` tool share
the same typed result:

```json
{
  "workspace": "minimal-agent",
  "state": "stopped",
  "ok": true
}
```

`ok` is `true` for a clean finish (`stopped`, `halted`, or `prepared`) and
`false` for `failed` or `quarantined` - the same rule the exit code follows.

## What it waits for

`wait` returns as soon as the workspace is in a state that cannot progress
without another lifecycle verb:

- `stopped`, `halted` - the run finished or was shut down cleanly; exit `0`
- `prepared` - the workspace was created but never started, so there is no
  run to wait for; returns immediately with exit `0`
- `failed` - the run ended abnormally; exit `1`
- `quarantined` - execution was frozen, authority severed, evidence attempted, and the VM stopped into custody; exit `1`

While the recorded state is live (`starting`, `running`, `stopping`), each
check reconciles against the backend supervisor the same way `status` does.
A VM whose process died therefore resolves to its real terminal state instead
of blocking on a stale `running` record. A `paused` workspace is not terminal:
`wait` keeps waiting until it is resumed and finishes, hits `--timeout`, or
is interrupted.

## Flags

| Flag | Description |
|---|---|
| `--timeout <dur>` | Give up after this long (Go duration, for example `30s` or `5m`); `0` (default) waits forever |
| `--interval <dur>` | Delay between state checks (default `1s`) |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](index.md#global-flags) for `--output`/`--json`/`--supervisor`.

## Exit status

`wait` exits `0` when the workspace ends in `stopped`, `halted`, or
`prepared`, and `1` when it ends in `failed` or `quarantined` (the
terminal-state JSON above is still written first). It exits nonzero with an
error when the workspace does not exist, `--timeout` elapses, or the wait is
interrupted.
Over MCP, a timeout maps to a retryable `transient` error and a missing
workspace maps to `not_found`.

## Related

- [`start`](start.md) - `start --wait` boots and waits in one command
- [`status`](status.md) - the point-in-time view `wait` polls for you
- [`result`](result.md) - read the structured result after the run finishes
- [State and identity](../concepts/state-and-identity.md) - the state model behind the terminal states
