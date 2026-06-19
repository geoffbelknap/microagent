---
title: microagent result
description: Show the structured result for a workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-19_

```text
microagent result <name> [--state-dir <dir>]
```

`result` reads the guest result channel for one workspace and returns the
completion payload - start/completion timestamps, exit code, stdout, stderr,
and failure error when the guest reported one - separately from serial logs.
It answers what the guest command produced; use [`status`](/cli/status/) for
the workspace's current state and readiness, and [`events`](/cli/events/) for
the lifecycle history that led there.

## Examples

Read the result:

```bash
microagent --json result research
```

When a result is ready, the response carries it under `result`:

```json
{
  "ok": true,
  "backend": "linux-kvm",
  "result": {
    "identity": { "runtimeID": "research", "role": "workload", "backend": "linux-kvm" },
    "resultPath": "/home/user/.microagent/workspaces/research/result.json",
    "startedAt": "2026-06-01T12:00:00Z",
    "completedAt": "2026-06-01T12:00:03Z",
    "exitCode": 0,
    "stdout": "ok\n"
  }
}
```

## Flags

You'll rarely need flags here - the global `--json` before the subcommand is
the one that matters.

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name (also accepted as positional) |
| `--id <id>` | Workspace ID alias for `--name` |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`/`--supervisor`.

## Exit status

`result` exits `0` when the workspace is found and the result channel is read;
nonzero when the workspace cannot be found or no result has been delivered. In
AX mode a failure is written as a structured error envelope.

## Related

- [`status`](/cli/status/) - current state; includes `result` when ready
- [`events`](/cli/events/) - the lifecycle history
- [`logs`](/cli/logs/) - the serial console output
