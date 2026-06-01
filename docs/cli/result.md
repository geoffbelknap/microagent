---
title: microagent result
description: Show the structured result for a workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

```text
microagent result <name> [--state-dir <dir>]
```

`result` reads the guest result channel for one workspace. It returns the
completion payload separately from serial logs.

With the global `--json` flag, the response includes `result` with identity, backend, result
path, start/completion timestamps, exit code, stdout, stderr, and failure
error when the guest reported one.

## Flags

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name (also accepted as positional) |
| `--id <id>` | Workspace ID alias for `--name` |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--json` | Global flag before `result`; print structured JSON output |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`/`--supervisor`.

## Example

```bash
microagent --json result research
```

When a result is ready, the response carries it under `result`:

```json
{
  "ok": true,
  "backend": "firecracker",
  "result": {
    "identity": { "runtimeID": "research", "role": "workload", "backend": "firecracker" },
    "resultPath": "/home/user/.microagent/workspaces/research/result.json",
    "startedAt": "2026-06-01T12:00:00Z",
    "completedAt": "2026-06-01T12:00:03Z",
    "exitCode": 0,
    "stdout": "ok\n"
  }
}
```

## Related

- [`status`](/cli/status/), [`logs`](/cli/logs/)
