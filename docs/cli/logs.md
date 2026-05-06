---
title: microagent logs
description: Show boot/serial output for a workspace.
---

```text
microagent logs <name> [--state-dir <dir>]
```

`logs` prints the captured serial console output for a workspace. Use
[`connect`](/cli/connect/) when you need an interactive console.

## Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory holding the workspace record |

## Example

```bash
microagent logs research
```

## Related

- [`connect`](/cli/connect/) for an interactive console
