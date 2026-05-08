---
title: microagent result
description: Show the structured result for a workspace.
---

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
| `--state-dir <dir>` | State directory holding the workspace record |
| `--json` | Global flag before `result`; print structured JSON output |

## Example

```bash
microagent --json result research
```

## Related

- [`status`](status.md), [`logs`](logs.md)
