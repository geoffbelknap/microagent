---
title: microagent delete
description: Remove a workspace and its state.
---

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
| `--state-dir <dir>` | State directory holding the workspace record |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--yes`, `-y` | Confirm deletion without prompting |
| `--force`, `-f` | Kill a running workspace before deleting |

## Example

```bash
microagent delete research
```

Non-interactive cleanup:

```bash
microagent delete research --yes
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
