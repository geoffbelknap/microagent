---
title: microagent events
description: Show or stream a workspace's lifecycle event history.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

```text
microagent events <name> [--follow] [--state-dir <dir>]
```

`events` prints the recorded lifecycle events for a workspace, oldest first.
Each event is a state transition (`prepared`, `starting`, `running`, `halted`,
`stopped`, `quarantined`, `failed`) with its timestamp and a short detail. The
history is the same `events.json` append log referenced by the
[supervisor protocol](/protocol/).

By default `events` prints the recorded history once. With `--follow` (`-f`) it
prints the history and then streams new events as the workspace changes state,
returning when the workspace reaches a terminal state (`halted`, `stopped`, or
`failed`) or you interrupt with Ctrl-C. With the global `--json` flag the events
are returned once as an array under `events`; `--follow` is not supported with
JSON/AX output.

## Flags

| Flag | Description |
|---|---|
| `--follow`, `-f` | Stream new events until the workspace reaches a terminal state or you interrupt |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Example

```bash
microagent events research
```

```text
2026-06-01T00:00:00Z  prepared  workspace state/config exists but runtime is not started
2026-06-01T00:00:01Z  running   runtime is started
2026-06-01T00:00:09Z  halted    clean disk-preserving shutdown completed
```

Follow a workspace through start and shutdown:

```bash
microagent events research --follow
```

## Related

- [`logs`](/cli/logs/) for serial console output
- [`status`](/cli/status/) for the current state and readiness
