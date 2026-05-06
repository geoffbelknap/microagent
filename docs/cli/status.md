---
title: microagent status
description: Show the current state of a workspace.
---

```text
microagent status <name> [--state-dir <dir>]
microagent status --name <name> [--state-dir <dir>]
```

`status` reads the state file for one workspace and prints the latest event:
identity, state (`prepared`, `running`, `stopped`, `killed`, `deleted`), and
backend.

## Flags

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name (also accepted as positional) |
| `--state-dir <dir>` | State directory holding the workspace record |
| `--supervisor <path>` | Override the active backend supervisor path |
| `--json` | Print structured JSON output |

## Examples

```bash
microagent status --name research
microagent status agent-1 --state-dir /tmp/microagent-kit --json
```

## Related

- [`ps`](/cli/ps/) for a list view
- [State and identity](/concepts/state-and-identity/)
