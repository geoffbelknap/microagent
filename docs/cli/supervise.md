---
title: microagent supervise
description: Start and restart a workspace according to its restart policy.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

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
