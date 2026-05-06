---
title: microagent connect
description: Open the workspace console (Apple VF only).
---

```text
microagent connect <name> [--send "<line>"] [--state-dir <dir>]
```

`connect` opens an interactive serial console for a workspace. With `--send`
it writes one line to the console and prints any new output, which is useful
in scripts.

`connect` is supported by Apple VF only. For Firecracker workspaces, use
[`logs`](/cli/logs/) for serial output.

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
