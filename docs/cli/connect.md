---
title: microagent connect
description: Open an interactive console shell inside a workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-13_

```text
microagent connect <name> [--send "<line>"] [--state-dir <dir>] [--timeout <seconds>] [--ready-timeout <seconds>]
```

`connect` opens an interactive serial console for a workspace - the path for a
human at a keyboard. With `--send` it writes one line to the console and prints
any new output. When a script or agent needs typed stdout/stderr/exit-code
results, use [`exec`](/cli/exec/) instead.

In interactive mode, press `Ctrl-]` (or the docker-style `Ctrl-P Ctrl-Q`
sequence) to detach from the console without stopping the workspace. Typing
`exit` closes the current guest shell and returns from `connect`; the workspace
stays running unless you run a shutdown command such as `poweroff`.

Interactive sessions are not supported in [AX mode](/concepts/glossary/); the
command is rejected with an error telling you to use `connect --send`, which
returns structured output.

Use [`logs`](/cli/logs/) when you want captured serial output instead of an
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
`--timeout` seconds (default 5); a timeout means the command did not report
completion before the deadline, and the command exits with an error that
includes any partial output that was captured. If the shell prompt never
appears, `connect` exits with an error saying the guest shell is not ready -
check [`logs`](/cli/logs/) for boot progress.

## Flags

Flags you'll actually use:

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

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Choosing the shell and hostname

The console starts `/bin/sh` by default. Set `--shell <path>` on
[`create`](/cli/create/) or `shell:` in a workspace spec to use another shell,
such as `/bin/bash`. The shell path must exist inside the guest rootfs.

The guest hostname defaults to the workspace name sanitized as a Linux hostname.
Use `--hostname <name>` or `hostname:` in the spec to override it.

The host-level `consoleAvailable` field means the backend supports an
interactive console on this machine. It is not a guarantee that a specific
workspace is ready for input; the workspace must be running and `shellReady`
must report that the shell target is reachable.

## Exit status

`connect` exits `0` when the session or `--send` exchange completes; nonzero
when the backend console endpoint or guest shell is not ready, or - with
`--send` - when the command does not report completion before the deadline. On a
`--send` timeout the error includes any partial output that was captured. In AX
mode these return structured error envelopes (a console read timeout is
reported as a `transient` error with `partial_output`).

## Related

- [`exec`](/cli/exec/) - typed results for scripts and agents
- [`logs`](/cli/logs/) - the captured serial output
- [`status`](/cli/status/) - check `shellReady` before connecting
