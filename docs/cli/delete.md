---
title: microagent delete
description: Remove a workspace and everything it owns on disk.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-29_

```text
microagent delete <name> [<name>...] [--yes] [--force] [--state-dir <dir>]
```

`delete` removes the workspace record and its on-disk artifacts (rootfs,
bundles, state file). It's the end of the line - to shut a workspace down and
keep it, use [`halt`](/cli/halt/) instead.

Several names delete in one call, with one confirmation for the whole batch
and a result line per workspace. A failure on one workspace does not stop
the others, and the exit status reports whether any failed.

By default, `delete` asks for confirmation. If the workspace is running, the
prompt becomes "Stop and delete it?". Either `--yes` or `--force` skips the
prompt; on a running workspace, `--yes` stops it gracefully before deleting,
while `--force` kills it instead.

Delete is idempotent, and says what it did: deleting a workspace that does
not exist (or was already deleted) exits 0, so retried teardown never fails
on "already gone". But it reports "did not exist; nothing deleted" (JSON:
`"deleted": false`) rather than pretending a removal happened, so a typo'd
name or an unexpanded shell glob can't masquerade as a successful cleanup.
Nothing existed to lose, so no confirmation is asked.

## Examples

Delete a workspace (asks for confirmation):

```bash
microagent delete research
```

Delete several at once (one prompt for the batch):

```bash
microagent delete research scratch demo
```

Non-interactive cleanup:

```bash
microagent delete research --yes
microagent delete research -y
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

Common flags:

- `--yes` / `-y` - skip the confirmation prompt in scripts; stops a running
  workspace before deleting
- `--force` / `-f` - also skips the prompt, but kills a running workspace
  instead of stopping it

The complete set:

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name; positional name is also accepted |
| `--id <id>` | Workspace ID alias for `--name` |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--yes`, `-y` | Confirm deletion without prompting |
| `--force`, `-f` | Skip the prompt and kill a running workspace before deleting |

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--supervisor`.

## Exit status

`delete` exits `0` when every named workspace is removed or was already
absent; nonzero when any workspace cannot be removed, or when a running
workspace cannot be stopped or killed before deletion. A non-interactive run
without `--yes` or `--force` that would require confirmation also fails
rather than prompting blindly.

## Related

- [`halt`](/cli/halt/) - shut down without removing state (`stop` is an alias)
- [`kill`](/cli/kill/) - force-terminate first when needed
- [`list`](/cli/list/) - see what's left
