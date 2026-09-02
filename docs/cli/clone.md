---
title: microagent clone
description: Copy a stopped workspace into a new workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-15_

```text
microagent clone <source> <target> [--state-dir <dir>]
```

`clone` copies a prepared, halted, or stopped workspace into a new workspace record. The
target gets its own rootfs and workspace-owned disks. Runtime process state is
not copied - for a running-state fork, see
[`create --from-snapshot`](create.md#fork-from-a-snapshot).

In a terminal, a clone that takes long enough to notice reports copied bytes
on stderr. Its total is the logical size of the source workspace files, and
completion is reported only after the target workspace record is published.
JSON and MCP responses contain no terminal progress text.

## Examples

Keep a template workspace and clone working copies from it:

```bash
microagent clone template research
microagent start research
```

## Flags

`--state-dir` matters only when the workspaces live outside the default
`~/.microagent/`.

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory holding the workspace records (default `~/.microagent/`) |

`clone` operates on offline disks; it takes no backend or supervisor selection.

## Semantics

- The source must be prepared, halted, or stopped.
- The target workspace must not already exist.
- Files under `workspaces/<source>/` are copied.
- Disk paths inside the source workspace directory are rewritten to the target
  workspace directory.
- External disk paths are left unchanged.

## Exit status

`clone` exits `0` on success; nonzero when the source is missing, running, or paused,
the target already exists, or the copy fails.

## Related

- [`create`](create.md) - build a workspace from an image
- [`start`](start.md) - boot the clone
- [`list`](list.md) - list source and target side by side
