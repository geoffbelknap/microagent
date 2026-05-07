---
title: microagent stop
description: Shut a workspace down gracefully.
---

```text
microagent stop <name> [--state-dir <dir>]
```

`stop` requests a graceful shutdown. On Firecracker this sends SIGTERM to the
recorded VM process; on Apple VF it asks the supervisor to stop the VM.

## Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory holding the workspace record |
| `--supervisor <path>` | Override the active backend supervisor path |

## Example

```bash
microagent stop research
```

If the VM doesn't shut down cleanly, follow up with [`kill`](kill.md).

## Related

- [`kill`](kill.md), [`delete`](delete.md), [`status`](status.md)
