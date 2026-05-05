---
title: microagent ps
description: List all workspaces in the state directory.
---

```text
microagent ps [--state-dir <dir>]
```

`ps` walks the state directory and prints one row per workspace, with name,
backend, and current state.

## Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory to scan |
| `--json` | Print structured JSON output |

## Example

```bash
microagent ps
microagent ps --json
```

## Related

- [`status`](/cli/status/) for a single workspace
