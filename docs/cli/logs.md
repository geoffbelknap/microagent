---
title: microagent logs
description: Read or follow a workspace's captured serial console output.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-15_

```text
microagent logs <name> [--follow] [--state-dir <dir>]
```

`logs` prints the captured serial console output for a workspace. It is useful
for boot diagnostics and for reviewing output after an interactive
[`connect`](/cli/connect/) session.

`microagent log` is accepted as an alias for `logs`.

By default `logs` reads the full captured serial buffer once and prints it. With
`--follow` (`-f`) it prints the buffer and then streams new output as it is
appended, returning when the workspace leaves the running state or you interrupt
with Ctrl-C. With the global `--json` flag, the buffer is returned once as a
string under `logs`; `--follow` is not supported with JSON output.

Follow mode may show one delayed connection indicator on stderr. It is closed
before the captured buffer or first new log byte is written, and no periodic
spinner frames appear in the log stream.

## Examples

Read the serial buffer:

```bash
microagent logs research
```

Follow new output as it appears:

```bash
microagent logs research --follow
```

Typical serial output begins with the guest boot log:

```text
[    0.000000] Linux version 6.1.0 ...
[    0.512000] Run /init as init process
microagent: guest init started
research login:
```

## Flags

Add `--follow` when you're watching a boot or a long-running guest.
`--state-dir` matters only for a non-default state directory.

| Flag | Description |
|---|---|
| `--follow`, `-f` | Stream the buffer and new output until the workspace stops or you interrupt |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--output`/`--json`.

## Exit status

`logs` exits `0` after printing the buffer (or when a `--follow` stream ends
normally); nonzero when the workspace cannot be found or the serial log cannot
be read.

## Related

- [`connect`](/cli/connect/) - an interactive console instead of captured output
