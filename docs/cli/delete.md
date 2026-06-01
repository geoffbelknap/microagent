---
title: microagent delete
description: Remove a workspace and its state.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

```text
microagent delete <name> [--yes] [--force] [--state-dir <dir>]
microagent rm <name> [--yes] [--force] [--state-dir <dir>]
```

`delete` removes the workspace record and its on-disk artifacts (rootfs,
bundles, state file).

By default, `delete` asks for confirmation. If the workspace is running, it
asks whether to stop and delete it. Use `--yes` for non-interactive cleanup.
Use `--force` to kill a running workspace before deleting it.

`rm` is a familiar alias for `delete`; `-f`/`--force` and `-y`/`--yes` have
the same behavior.

## Flags

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name; positional name is also accepted |
| `--id <id>` | Workspace ID alias for `--name` |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--yes`, `-y` | Confirm deletion without prompting |
| `--force`, `-f` | Kill a running workspace before deleting |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`/`--supervisor`.

## Exit status

`delete` exits nonzero when the workspace cannot be found or removed, or when a
running workspace cannot be stopped or killed before deletion. A non-interactive
run without `--yes` that would require confirmation also fails rather than
prompting blindly. In AX mode a failure is written as a structured error
envelope (a missing workspace maps to `not_found`).

## Example

```bash
microagent delete research
```

Non-interactive cleanup:

```bash
microagent delete research --yes
microagent rm research -y
```

Force-delete a running workspace:

```bash
microagent delete research --force
```

Lower-level form:

```bash
microagent delete agent-1 --state-dir /tmp/microagent
```

## Related

- [`stop`](/cli/stop/), [`kill`](/cli/kill/), [`ps`](/cli/ps/)
