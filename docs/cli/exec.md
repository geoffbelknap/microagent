---
title: microagent exec
description: Run a structured command in a running workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

```text
microagent exec <workspace> [flags] -- <argv...>
microagent --mode=ax exec <workspace> [flags] -- <argv...>
```

`exec` runs one command through the structured exec service in a running
workspace. It does not use the interactive console path. Command arguments after
`--` are passed as argv directly; use `sh -lc` explicitly when you want shell
syntax.

A command issued immediately after [`start`](/cli/start/) waits briefly for the
in-guest exec service to become ready (the post-start window where the host
forward is bound but the guest service is not yet listening), so the command is
not rejected by a transient connection error. The wait runs an idempotent
readiness probe, so your command is still issued exactly once.

In UX mode, command stdout is written to stdout, command stderr is written to
stderr, and the CLI exits with the command exit code when the command exits
normally. Timeout, signal, and failed-to-start statuses use nonzero CLI exit
codes.

In AX mode, stdout is one JSON `ExecResult`. A nonzero command exit is still a
successful tool call and is reported in `exit_code`; the CLI exits nonzero only
when the exec request itself cannot complete.

## Flags

| Flag | Description |
|---|---|
| `--env KEY=VALUE`, `-e KEY=VALUE` | Environment variable for the command; repeatable |
| `--cwd <path>` | Working directory inside the workspace |
| `--timeout <duration>` | Command timeout, such as `30s` or `5m` |
| `--stdin <path>` or `-` | Read command stdin from a file, or from CLI stdin with `-` |
| `--stdout-limit <bytes>` | Stdout output limit in bytes |
| `--stderr-limit <bytes>` | Stderr output limit in bytes |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Exit status

In UX mode, `exec` exits with the guest command's exit code when the command
exits normally. Timeout, signal, and failed-to-start statuses use distinct
nonzero CLI exit codes, and a failure of the exec request itself (for example,
the workspace is not running) is a nonzero exit.

In AX mode, a nonzero command exit is still a successful tool call - reported in
`exit_code` - and the CLI exits `0`. The CLI exits nonzero only when the exec
request itself cannot complete, and then writes a structured error envelope.

## Examples

```bash
microagent exec research -- uname -a
microagent exec research -- sh -lc 'echo out; echo err >&2'
microagent --mode=ax exec research -- sh -c 'exit 7'
microagent exec research --stdin input.txt -- cat
```

## Related

- [`connect`](/cli/connect/) for the interactive console
- [`status`](/cli/status/) for `execReady`
