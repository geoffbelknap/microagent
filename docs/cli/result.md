---
title: microagent result
description: Show the structured result for a workspace.
---

```text
microagent result <name> [--state-dir <dir>]
```

`result` reads the guest result channel for one workspace. It returns the
machine-readable completion payload separately from serial logs.

With `--json`, the response includes `result` with identity, backend, result
path, start/completion timestamps, exit code, stdout, stderr, and failure
error when the guest reported one.

## Flags

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name (also accepted as positional) |
| `--state-dir <dir>` | State directory holding the workspace record |
| `--json` | Print structured JSON output |

## Example

```bash
microagent result research --json
```

## Related

- [`status`](/cli/status/), [`logs`](/cli/logs/)
