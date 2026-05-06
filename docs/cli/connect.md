---
title: microagent connect
description: Open the workspace console (Apple VF only).
---

```text
microagent connect <name> [--send "<line>"] [--state-dir <dir>] [--ready-timeout <seconds>]
```

`connect` opens an interactive serial console for a workspace. With `--send`
it writes one line to the console and prints any new output, which is useful
in scripts.

In interactive mode, press `Ctrl-]` to detach from the console without stopping
the workspace.

`connect` is supported by Apple VF only. For Firecracker workspaces, use
[`logs`](/cli/logs/) for serial output.

## Flags

| Flag | Description |
|---|---|
| `--send <line>` | Write one line to the console and print new output |
| `--timeout <seconds>` | Seconds to wait for output after `--send` |
| `--ready-timeout <seconds>` | Seconds to wait for a shell prompt before attaching or sending; `0` disables |
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

`connect` waits for the console FIFO and, by default, for a basic shell prompt
before attaching or writing. If the guest shell is not ready, it exits with an
error that points to [`logs`](/cli/logs/).

## Related

- [`logs`](/cli/logs/), [`status`](/cli/status/)
