---
title: microagent delete
description: Remove a workspace and everything it owns on disk.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

```text
microagent delete <name> [--yes] [--force] [--state-dir <dir>]
microagent rm <name> [--yes] [--force] [--state-dir <dir>]
```

`delete` removes the workspace record and its on-disk artifacts (rootfs,
bundles, state file). It's the end of the line - to shut a workspace down and
keep it, use [`halt`](/cli/halt/) instead.

By default, `delete` asks for confirmation. If the workspace is running, it
asks whether to stop and delete it. Use `--yes` for non-interactive cleanup.
Use `--force` to kill a running workspace before deleting it.

`rm` is a familiar alias for `delete`; `-f`/`--force` and `-y`/`--yes` have
the same behavior.

## Examples

Delete a workspace (asks for confirmation):

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

## Flags

Flags you'll actually use:

- `--yes` / `-y` - skip the confirmation prompt in scripts
- `--force` / `-f` - kill a running workspace first instead of refusing

The complete set:

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

`delete` exits `0` when the workspace and its artifacts are removed; nonzero
when the workspace cannot be found or removed, or when a running workspace
cannot be stopped or killed before deletion. A non-interactive run without
`--yes` that would require confirmation also fails rather than prompting
blindly. In AX mode a failure is written as a structured error envelope (a
missing workspace maps to `not_found`).

## Related

- [`stop`](/cli/stop/) - shut down without removing state
- [`kill`](/cli/kill/) - force-terminate first when needed
- [`ps`](/cli/ps/) - see what's left
