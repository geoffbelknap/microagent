---
title: microagent connect
description: Open the workspace console.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-17_

```text
microagent connect <name> [--send "<line>"] [--state-dir <dir>] [--ready-timeout <seconds>]
```

`connect` opens an interactive serial console for a workspace. With `--send`
it writes one line to the console and prints any new output, which is useful
in scripts.

In interactive mode, press `Ctrl-]` to detach from the console without stopping
the workspace. Typing `exit` closes the current guest shell and returns from
`connect`; the workspace stays running unless you run a shutdown command such as
`poweroff`.

`connect` is supported by Apple VF, Firecracker, and experimental
Windows-HyperV. Windows-HyperV uses Hyper-V sockets rather than WSL or QEMU.
[`logs`](/cli/logs/) remains available for captured serial output.

The console starts `/bin/sh` by default. Set `--shell <path>` on
[`create`](/cli/create/) or `shell:` in a workspace spec to use another shell,
such as `/bin/bash`. The shell path must exist inside the guest rootfs.

The guest hostname defaults to the workspace name sanitized as a Linux hostname.
Use `--hostname <name>` or `hostname:` in the spec to override it.

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

`connect` waits for the backend console endpoint and, by default, for a basic
shell prompt before attaching or writing. If the guest shell is not ready, it
exits with an error that points to [`logs`](/cli/logs/).

The host-level `consoleAvailable` field means the backend supports an
interactive console on this machine. It is not a guarantee that a specific
workspace is ready for input; the workspace must be running and past the shell
readiness gate.

## Related

- [`logs`](/cli/logs/), [`status`](/cli/status/)
