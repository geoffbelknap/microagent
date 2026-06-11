---
title: microagent exec
description: Run a command in a running workspace and get typed results back.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

```text
microagent exec <workspace> [flags] -- <argv...>
microagent --mode=ax exec <workspace> [flags] -- <argv...>
```

`exec` runs one command through the structured exec service in a running
workspace and gives you typed stdout, stderr, and an exit code - it does not
use the interactive console path. Use [`connect`](/cli/connect/) when you want
a human shell session; use `exec` when a script or agent needs the result.
Command arguments after `--` are passed as argv directly; use `sh -lc`
explicitly when you want shell syntax.

A command issued immediately after [`start`](/cli/start/) waits briefly for the
in-guest exec service to become ready (the post-start window where the host
forward is bound but the guest service is not yet listening), so the command is
not rejected by a transient connection error. The wait runs an idempotent
readiness probe, so your command is still issued exactly once.

In UX mode, command stdout is written to stdout, command stderr is written to
stderr, and the CLI exits with the command exit code when the command exits
normally. Timeout, signal, and failed-to-start statuses use nonzero CLI exit
codes.

In AX mode, stdout is one JSON envelope with `ok`, `result`, `retry_count`,
`retry_wall_clock_ms`, and matching `metadata` fields. The `result` field holds
the structured `ExecResult`. A nonzero command exit is still a successful tool
call and is reported in `result.exit_code`; the CLI exits nonzero only when the
exec request itself cannot complete.

## Examples

Run a command and use its exit code:

```bash
microagent exec research -- uname -a
microagent exec research -- sh -lc 'echo out; echo err >&2'
```

Get the structured envelope for agents:

```bash
microagent --mode=ax exec research -- sh -c 'exit 7'
```

Feed the command stdin from a file:

```bash
microagent exec research --stdin input.txt -- cat
```

## Flags

Flags you'll actually use:

- `-e KEY=VALUE` - set environment variables for the command
- `--cwd <path>` - run somewhere other than the guest's default directory
- `--timeout <duration>` - bound a command that might hang (`30s`, `5m`)
- `--stream` - watch a long command's output live instead of buffered
- `--stdin <path>` or `-` - feed the command input from a file or CLI stdin

The complete set:

| Flag | Description |
|---|---|
| `--env KEY=VALUE`, `-e KEY=VALUE` | Environment variable for the command; repeatable |
| `--cwd <path>` | Working directory inside the workspace |
| `--stream` | Stream stdout/stderr incrementally as the command runs (UX mode) |
| `--timeout <duration>` | Command timeout, such as `30s` or `5m` |
| `--stdin <path>` or `-` | Read command stdin from a file, or from CLI stdin with `-` |
| `--stdout-limit <bytes>` | Stdout output limit in bytes |
| `--stderr-limit <bytes>` | Stderr output limit in bytes |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Streaming

By default `exec` buffers output and returns one final result. With `--stream`,
the guest delivers stdout and stderr incrementally as the command runs, so a
long-running command's output appears live instead of all at once. The exec
protocol carries a sequence of chunk frames followed by a terminal result frame
that holds the status, exit code, timing, and truncation flags (the streamed
result does not re-send the output bytes). The per-stream output limits still
apply - output past the limit is dropped and the truncation flag is set.

`--stream` is a UX-mode convenience for incremental terminal output. AX mode
always emits one structured exec envelope and ignores `--stream`, since
interleaving raw bytes with the JSON envelope would not be machine-parseable.
The streaming transport is also available to Go callers via
`workspace.ExecStream`.

## Exit status

In UX mode, `exec` exits with the guest command's exit code when the command
exits normally. Timeout, signal, and failed-to-start statuses use distinct
nonzero CLI exit codes, and a failure of the exec request itself (for example,
the workspace is not running) is a nonzero exit.

In AX mode, a nonzero command exit is still a successful tool call - reported in
`result.exit_code` - and the CLI exits `0`. The CLI exits nonzero only when the
exec request itself cannot complete, and then writes a structured error envelope
with the same retry metadata fields. Transient exec transport failures are
retried by the shared workspace exec layer before the final result or error is
reported.

## Related

- [`connect`](/cli/connect/) - the interactive console path
- [`status`](/cli/status/) - check `execReady` first
