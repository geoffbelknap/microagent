---
title: microagent connect
description: Open an interactive console shell inside a workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-17_

```text
microagent connect <name> [--send "<line>"] [--state-dir <dir>] [--timeout <seconds>] [--ready-timeout <seconds>]
```

`connect` opens an interactive serial console for a workspace - the path for a
human at a keyboard. With `--send` it writes one line to the console and prints
any new output. When a script or agent needs typed stdout/stderr/exit-code
results, use [`exec`](exec.md) instead.

In interactive mode, press `Ctrl-]` (or the docker-style `Ctrl-P Ctrl-Q`
sequence) to detach from the console without stopping the workspace. Typing
`exit` closes the current guest shell and returns from `connect`; the workspace
stays running unless you run a shutdown command such as `poweroff`.
Detach keys work in both legacy terminal mode and the extended `CSI u` keyboard
mode used by full-screen tools such as coding agents.

`connect` sends the host terminal's initial rows and columns to the guest PTY
and follows later terminal resize events. Full-screen applications redraw at
the new size without reconnecting. The resize channel is negotiated: an older
guest continues as a byte-stream console at its original size, so recreate its
workspace with the current microagent release to enable live resizing.

Disconnecting also terminates processes that remain in the console shell's
session, including ordinary background jobs. Run durable workloads through the
workspace service configuration. If a diagnostic process intentionally needs
to outlive one console connection, detach it from the shell session explicitly:

```bash
microagent connect research --send \
  "setsid /usr/local/bin/diagnostic </dev/null >/tmp/diagnostic.log 2>&1 &"
```

Interactive sessions need text output. With `--json` or `--output json`, the
command is rejected with an error pointing at `connect --send`, which returns
structured output instead.

If opening the console takes long enough to notice, human output shows one
delayed connection indicator on stderr. It stops before console bytes begin;
no spinner frames are mixed into the interactive stream.

Use [`logs`](logs.md) when you want captured serial output instead of an
interactive console.

## Examples

Open an interactive console:

```bash
microagent connect research
```

Send one line and print the output:

```bash
microagent connect research --send "cat /etc/os-release"
microagent connect research --send "cat /workspace/status; uname -m"
```

`connect` waits for the backend console endpoint and, by default, for a basic
shell prompt before attaching or writing (`--ready-timeout`, default 10
seconds; `0` disables the wait). With `--send`, output is collected for
`--timeout` seconds (default 5). A timeout means the command did not report
completion before the deadline; the command then exits with an error that
includes any partial output that was captured. If the shell prompt never
appears, `connect` exits with an error saying the guest shell is not ready -
check [`logs`](logs.md) for boot progress.

## Flags

Common flags:

- `--send <line>` - one-shot console input without an interactive session
- `--timeout <seconds>` - how long `--send` waits for output (default `5`)
- `--ready-timeout <seconds>` - how long to wait for a shell prompt first
  (default `10`)

The complete set:

| Flag | Description |
|---|---|
| `--send <line>` | Write one line to the console and print new output |
| `--timeout <seconds>` | Seconds to wait for output after `--send` (default `5`) |
| `--ready-timeout <seconds>` | Seconds to wait for a shell prompt before attaching or sending (default `10`; `0` disables) |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |

See [global flags](index.md#global-flags) for `--output`/`--json`.

## Choosing the shell and hostname

The console starts `/bin/sh` by default. Set `--shell <path>` on
[`create`](create.md) or `shell:` in a workspace spec to use another shell,
such as `/bin/bash`. The shell path must exist inside the guest rootfs.

The guest hostname defaults to the workspace name sanitized as a Linux hostname.
Use `--hostname <name>` or `hostname:` in the spec to override it.

The host-level `consoleAvailable` field means the backend supports an
interactive console on this machine. It is not a guarantee that a specific
workspace is ready for input; the workspace must be running and `shellReady`
must report that the shell target is reachable.

## Exit status

`connect` exits `0` when the session or `--send` exchange completes. It exits
nonzero when the backend console endpoint or guest shell is not ready, or -
with `--send` - when the command does not report completion before the
deadline. On a
`--send` timeout the error includes any partial output that was captured.
Agent clients using MCP receive a retryable `transient` error with
`partial_output` for a console read timeout.

## Related

- [`exec`](exec.md) - typed results for scripts and agents
- [`logs`](logs.md) - the captured serial output
- [`status`](status.md) - check `shellReady` before connecting
