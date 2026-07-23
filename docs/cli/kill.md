---
title: microagent kill
description: Force-terminate a workspace that won't stop.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-23_

```text
microagent kill <name> [--state-dir <dir>]
```

`kill` is the hard variant of [`stop`](/cli/stop/). Use it when `stop` doesn't
return; `stop` never escalates on its own. For a clean shutdown of a healthy
workspace you intend to start again, use [`halt`](/cli/halt/) instead. The disk
state survives `kill`, but nothing inside the guest gets a chance to flush or
exit cleanly.

## Examples

Force-terminate a workspace:

```bash
microagent kill research
```

## Flags

You'll rarely need flags here - `--state-dir` only when the workspace lives
outside the default `~/.microagent/`.

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name; positional name is also accepted |
| `--id <id>` | Workspace ID alias for `--name` |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--mode`/`--supervisor`.

## Exit status

`kill` exits `0` on success; nonzero when the workspace cannot be found or the
VM process cannot be terminated. In AX mode a failure is written as a
structured error envelope.

## Related

- [`stop`](/cli/stop/) - the graceful variant
- [`halt`](/cli/halt/) - park a healthy workspace cleanly
- [`delete`](/cli/delete/) - remove the workspace afterwards
