---
title: microagent logs
description: Show boot/serial output for a workspace.
---

```text
microagent logs <name> [--state-dir <dir>]
```

`logs` prints the captured serial console output for a workspace. It is useful
for boot diagnostics and for reviewing output after an interactive
[`connect`](connect.md) session.

## Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory holding the workspace record |

## Example

```bash
microagent logs research
```

## Related

- [`connect`](connect.md) for an interactive console
