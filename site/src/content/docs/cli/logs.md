---
title: microagent logs
description: Show boot/serial output for a workspace.
---

```text
microagent logs <name> [--state-dir <dir>]
```

`logs` prints the captured serial console output for a workspace. On
Firecracker this is the primary way to see guest output, since `connect` is
not available.

## Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory holding the workspace record |

## Example

```bash
microagent logs research
```

## Related

- [`connect`](/cli/connect/) for an interactive console (Apple VF only)
