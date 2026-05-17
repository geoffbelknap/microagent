---
title: microagent clone
description: Copy a stopped workspace into a new workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-08_

```text
microagent clone <source> <target> [--state-dir <dir>]
```

`clone` copies a prepared or stopped workspace into a new workspace record. The
target gets its own rootfs and workspace-owned disks. Runtime process state is
not copied.

## Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory holding the workspace records |

## Semantics

- The source must be prepared or stopped.
- The target workspace must not already exist.
- Files under `workspaces/<source>/` are copied.
- Disk paths inside the source workspace directory are rewritten to the target
  workspace directory.
- External disk paths are left unchanged.

## Example

```bash
microagent clone template research
microagent start research
```

## Related

- [`create`](/cli/create/), [`start`](/cli/start/), [`ps`](/cli/ps/)
