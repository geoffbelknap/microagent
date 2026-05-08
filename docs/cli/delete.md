---
title: microagent delete
description: Remove a workspace and its state.
---

```text
microagent delete <name> [--state-dir <dir>]
```

`delete` removes the workspace record and its on-disk artifacts (rootfs,
bundles, state file).

For Firecracker, `delete` refuses to remove state while the recorded VM
process is still running. Use [`stop`](stop.md) or [`kill`](kill.md)
first.

## Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory holding the workspace record |
| `--supervisor <path>` | Override the active backend supervisor path |

## Example

```bash
microagent stop research
microagent delete research
```

Lower-level form:

```bash
microagent delete agent-1 --state-dir /tmp/microagent-kit
```

## Related

- [`stop`](stop.md), [`kill`](kill.md), [`ps`](ps.md)
