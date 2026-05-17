---
title: microagent ps
description: List all workspaces in the state directory.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-17_

```text
microagent ps [--state-dir <dir>]
```

`ps` walks the state directory and prints one row per workspace, with name,
backend, and current state.

## Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory to scan |
| `--json` | Global flag before `ps`; print structured JSON output |

## Example

```bash
microagent ps
microagent --json ps
```

## Related

- [`status`](/cli/status/) for a single workspace
