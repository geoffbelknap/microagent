---
title: microagent logs
description: Show boot/serial output for a workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

```text
microagent logs <name> [--state-dir <dir>]
```

`logs` prints the captured serial console output for a workspace. It is useful
for boot diagnostics and for reviewing output after an interactive
[`connect`](/cli/connect/) session.

`logs` reads the full captured serial buffer once and prints it - it does not
follow or stream. Re-run it to see newer output. With the global `--json` flag,
the buffer is returned as a string under `logs`.

## Flags

| Flag | Description |
|---|---|
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
