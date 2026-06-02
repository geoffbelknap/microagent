---
title: microagent supervise
description: Start and restart a workspace according to its restart policy.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-02_

```text
microagent supervise <name> [--state-dir <dir>] [--max-restarts <n>]
```

`supervise` is a foreground host supervisor. It starts a workspace and keeps
watching it while the command is running. When the workspace reaches a terminal
state, the persisted restart policy decides whether `supervise` starts it
again.

## Restart policies

| Policy | Behavior |
|---|---|
| `never` | Do not start under `supervise` |
| `on-failure` | Restart only after a `failed` state |
| `always` | Restart after `stopped` or `failed` |

The policy comes from `microagent create --restart ...` or `restart:` in
`microagent.yaml`.

## Health checks

By default `supervise` only restarts a workspace that exits — it cannot tell an
alive-but-wedged guest from a healthy one. Declare a [`health`](/cli/spec/)
probe in `microagent.yaml` to close that gap:

```yaml
restart: on-failure
health:
  exec: ["python3", "-c", "import socket; socket.create_connection(('127.0.0.1', 8080), 1)"]
  intervalSeconds: 15
  retries: 3
  startPeriodSeconds: 10
```

While the workspace runs, `supervise` probes it every `intervalSeconds` (after a
`startPeriodSeconds` grace). After `retries` consecutive failures the workspace
is force-killed and the restart policy restarts it — so health-based restart
requires `on-failure` or `always`. Probe forms:

- `exec` — a command run in the guest via structured exec (Firecracker only);
  healthy on exit 0.
- `httpGet` + `port` — a host-side GET against a published guest port; healthy
  on a non-error status.

An unhealthy probe surfaces as a `failed` state in the supervise result, so the
restart accounting (and `--max-restarts`) applies the same as an exit failure.

## Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--backend <name>` | Backend identity override |
| `--arch <arch>` | Guest architecture |
| `--kernel <path>` | Kernel path |
| `--interval <seconds>` | Seconds between state checks |
| `--max-restarts <n>` | Maximum restarts; `0` means unlimited |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`/`--supervisor`.

## Example

```bash
microagent create research --restart always
microagent supervise research
```

Stop the foreground `supervise` process to stop restart supervision.

## Related

- [`create`](/cli/create/)
- [`start`](/cli/start/)
- [`status`](/cli/status/)
