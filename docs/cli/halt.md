---
title: microagent halt
description: Cleanly stop a workspace while preserving disk state.
---

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
| `--state-dir <dir>` | State directory holding the workspace record |
| `--supervisor <path>` | Override the active backend supervisor path |

## Example

```bash
microagent halt research
microagent start research
```

## Related

- [`start`](start.md), [`stop`](stop.md), [`kill`](kill.md), [`status`](status.md)
