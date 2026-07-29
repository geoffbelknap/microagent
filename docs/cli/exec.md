---
title: microagent exec
description: Run a command in a running workspace and get typed results back.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-29_

```text
microagent exec <workspace> [flags] -- <argv...>
```

`exec` runs one command through the structured exec service in a running
workspace and gives you typed stdout, stderr, and an exit code - it does not
use the interactive console path. Use [`connect`](/cli/connect/) when you want
a human shell session; use `exec` when a script or agent needs the result.
Command arguments after `--` are passed as argv directly; use `sh -lc`
explicitly when you want shell syntax.

A command issued immediately after [`start`](/cli/start/) waits briefly for the
in-guest exec service to become ready, so the command is not rejected by a
transient connection error. (This covers the post-start window where the host
forward is bound but the guest service is not yet listening.) The wait runs an
idempotent readiness probe, so your command is still issued exactly once.

By default, command stdout and stderr are written to your stdout and stderr.
With `--json`, the CLI serializes the typed exec result, including protocol
version, start/completion timestamps, retry accounting, and truncation flags:

```json
{
  "status": "exited",
  "exit_code": 0,
  "stdout": "bGludXgK",
  "stderr": ""
}
```

`stdout`/`stderr` are base64-encoded. Agents should call the MCP
`workspace.exec` tool, which presents the same typed operation with
agent-oriented retry metadata and actionable errors.

## Examples

Run a command and use its exit code:

```bash
microagent exec research -- uname -a
microagent exec research -- sh -lc 'echo out; echo err >&2'
```

Get structured output for a script:

```bash
microagent --json exec research -- sh -c 'exit 7'
```

Feed the command stdin from a file:

```bash
microagent exec research --stdin input.txt -- cat
```

## Flags

Common flags:

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
| `--stream` | Stream stdout/stderr incrementally as the command runs |
| `--timeout <duration>` | Command timeout, such as `30s` or `5m` |
| `--stdin <path>` or `-` | Read command stdin from a file, or from CLI stdin with `-` |
| `--stdout-limit <bytes>` | Stdout output limit in bytes |
| `--stderr-limit <bytes>` | Stderr output limit in bytes |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--output`/`--json`.

## Streaming

By default `exec` buffers output and returns one final result. With `--stream`,
the guest delivers stdout and stderr incrementally as the command runs, so a
long-running command's output appears live instead of all at once. The exec
protocol carries a sequence of chunk frames followed by a terminal result frame
that holds the status, exit code, timing, and truncation flags (the streamed
result does not re-send the output bytes). The per-stream output limits still
apply - output past the limit is dropped and the truncation flag is set.

`--stream` is a convenience for incremental terminal output. With JSON output,
exec emits one structured result and ignores `--stream`, since interleaving
raw bytes with JSON would not be machine-parseable.
The streaming transport is also available to Go callers via
`workspace.ExecStream`.

## Exit status

`exec` exits with the guest command's exit code when the command
exits normally. Timeout, signal, and failed-to-start statuses use distinct
nonzero CLI exit codes, and a failure of the exec request itself (for example,
the workspace is not running) is a nonzero exit.

## Related

- [`connect`](/cli/connect/) - the interactive console path
- [`status`](/cli/status/) - check `execReady` first
