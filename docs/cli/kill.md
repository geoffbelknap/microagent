---
title: microagent kill
description: Force-stop a workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-17_

```text
microagent kill <name> [--state-dir <dir>]
```

`kill` is the hard variant of [`stop`](/cli/stop/). On Firecracker it sends
SIGKILL to the recorded VM process; on Apple VF it asks the supervisor to
terminate the VM immediately. Use it when `stop` doesn't return.

## Flags

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name; positional name is also accepted |
| `--id <id>` | Workspace ID alias for `--name` |
| `--state-dir <dir>` | State directory holding the workspace record |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |

## Example

```bash
microagent kill research
```

## Related

- [`stop`](/cli/stop/), [`delete`](/cli/delete/)
