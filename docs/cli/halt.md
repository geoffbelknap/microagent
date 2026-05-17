---
title: microagent halt
description: Cleanly stop a workspace while preserving disk state.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-17_

```text
microagent halt <name> [--state-dir <dir>]
```

`halt` requests a clean shutdown and records the terminal state as `halted`.
The VM process exits, but the workspace rootfs, attached disks, identity, and
event timeline remain under `--state-dir` so a later `microagent start <name>`
boots the same disk state.

This is not memory pause/resume. A restarted workspace boots again from the
preserved disk.

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
microagent halt research
microagent start research
```

## Related

- [`start`](/cli/start/), [`stop`](/cli/stop/), [`kill`](/cli/kill/), [`status`](/cli/status/)
