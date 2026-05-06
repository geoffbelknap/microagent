---
title: microagent connect
description: Open the workspace console.
---

```text
microagent connect <name> [--send "<line>"] [--state-dir <dir>]
```

`connect` opens an interactive serial console for a workspace. With `--send`
it writes one line to the console and prints any new output, which is useful
in scripts.

Use [`logs`](/cli/logs/) when you only need to read serial output without
opening the console.

## Flags

| Flag | Description |
|---|---|
| `--send <line>` | Write one line to the console and print new output |
| `--state-dir <dir>` | State directory holding the workspace record |

## Examples

Interactive console:

```bash
microagent connect research
```

Script-friendly:

```bash
microagent connect research --send "cat /etc/os-release"
microagent connect research --send "cat /workspace/status; uname -m"
```

## Related

- [`logs`](/cli/logs/), [`status`](/cli/status/)
