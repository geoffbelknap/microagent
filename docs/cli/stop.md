---
title: microagent stop
description: Shut a workspace down gracefully.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

```text
microagent stop <name> [--state-dir <dir>]
```

`stop` requests a graceful shutdown. On Firecracker this sends SIGTERM to the
recorded VM process; on Apple VF it asks the supervisor to stop the VM.

## Flags

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name; positional name is also accepted |
| `--id <id>` | Workspace ID alias for `--name` |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`/`--supervisor`.

## Example

```bash
microagent stop research
```

If the VM doesn't shut down cleanly, follow up with [`kill`](/cli/kill/).

## Related

- [`kill`](/cli/kill/), [`delete`](/cli/delete/), [`status`](/cli/status/)
