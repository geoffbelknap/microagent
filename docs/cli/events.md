---
title: microagent events
description: Show or stream a workspace's event history.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-23_

```text
microagent events <name> [--follow] [--state-dir <dir>]
```

`events` prints the recorded events for a workspace, oldest first. Most events
are state transitions (`prepared`, `starting`, `running`, `halted`, `stopped`,
`quarantined`, `failed`) with their timestamp and a short detail. The history
is the same `events.json` append log described in
[State and identity](/concepts/state-and-identity/). It is the history view:
[`status`](/cli/status/) answers what state the workspace is in now,
[`result`](/cli/result/) returns the guest's completion payload, and `events`
shows how the workspace got here.

Workspaces paired with host model runners also append model-worker markers when
the runner is attached or released. These entries keep the same identity,
state, timestamp, and detail shape as lifecycle events, with details such as
`model_worker=attached` or `model_worker=released`. The detail records model
and runner metadata needed for operator tracing, including the runner config
digest, but not runner environment values.

By default `events` prints the recorded history once. With `--follow` (`-f`) it
prints the history and then streams new events as the workspace changes state,
returning when the workspace reaches a terminal state (`halted`, `stopped`, or
`failed`) or you interrupt with Ctrl-C. With the global `--json` flag the events
are returned once as an array under `events`; `--follow` is not supported with
JSON/AX output.

## Examples

Show the recorded history:

```bash
microagent events research
```

```text
2026-06-01T00:00:00Z  prepared  workspace state/config exists but runtime is not started
2026-06-01T00:00:00Z  starting   model_worker=attached model_ref=hf.co/example/model.gguf engine=runner pid=1234 runner_config_digest=sha256:abc... holder=research model_url=http://127.0.0.1:6017/v1
2026-06-01T00:00:01Z  running   runtime is started
2026-06-01T00:00:09Z  halted    clean disk-preserving shutdown completed
2026-06-01T00:00:09Z  halted    model_worker=released model_ref=hf.co/example/model.gguf holder=research
```

Follow a workspace through start and shutdown:

```bash
microagent events research --follow
```

## Flags

You'll rarely need flags here - `--follow` when you want to watch transitions
live instead of reading the history once.

| Flag | Description |
|---|---|
| `--follow`, `-f` | Stream new events until the workspace reaches a terminal state or you interrupt |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--mode`.

## Exit status

`events` exits `0` when the workspace record is found and read; nonzero when
the workspace cannot be found or `--follow` is combined with JSON/AX output. In
AX mode a failure is written as a structured error envelope.

## Related

- [`status`](/cli/status/) - the current state and readiness
- [`result`](/cli/result/) - the completion payload
- [`logs`](/cli/logs/) - serial console output
