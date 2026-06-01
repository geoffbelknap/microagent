---
title: microagent logs
description: Show boot/serial output for a workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

```text
microagent logs <name> [--follow] [--state-dir <dir>]
```

`logs` prints the captured serial console output for a workspace. It is useful
for boot diagnostics and for reviewing output after an interactive
[`connect`](/cli/connect/) session.

By default `logs` reads the full captured serial buffer once and prints it. With
`--follow` (`-f`) it prints the buffer and then streams new output as it is
appended, returning when the workspace leaves the running state or you interrupt
with Ctrl-C. With the global `--json` flag, the buffer is returned once as a
string under `logs`; `--follow` is not supported with JSON/AX output.

## Flags

| Flag | Description |
|---|---|
| `--follow`, `-f` | Stream the buffer and new output until the workspace stops or you interrupt |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Example

```bash
microagent logs research
```

Typical serial output begins with the guest boot log:

```text
[    0.000000] Linux version 6.1.0 ...
[    0.512000] Run /init as init process
microagent: guest init started
research login:
```

## Related

- [`connect`](/cli/connect/) for an interactive console
