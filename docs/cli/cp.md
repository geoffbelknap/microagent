---
title: microagent cp
description: Copy a file into or out of a stopped workspace's disks.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-15_

```text
microagent cp <source> <target> [--state-dir <dir>] [--debugfs <path>]
```

`cp` copies one regular file between the host and an offline workspace disk.
It is not a sync daemon and it does not attach to a running VM - the workspace
must be prepared, halted, or stopped. To get output from a running workspace, use
[`exec`](exec.md) or declared `--output` artifact paths instead.

In a terminal, a copy that takes long enough to notice reports the current
phase and transferred bytes on stderr. Completed byte counts use the source
file size as their total. JSON and MCP responses contain no terminal progress
text.

## Examples

Copy into the rootfs:

```bash
microagent cp ./config.json research:/etc/microagent/config.json
```

Copy from the rootfs:

```bash
microagent cp research:/var/log/boot.log ./boot.log
```

Copy into an attached disk named `workspace`:

```bash
microagent cp ./notes.txt research:workspace:/notes.txt
```

## Endpoints

Exactly one endpoint must be a workspace endpoint:

| Form | Meaning |
|---|---|
| `<workspace>:/absolute/path` | Rootfs path |
| `<workspace>:<disk>:/absolute/path` | Attached disk path |
| `/host/path` | Host path |

## Flags

`--debugfs` matters only when `debugfs` is not on `PATH`, and `--state-dir`
only for a non-default state directory.

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |
| `--debugfs <path>` | `debugfs` binary path when it is not on `PATH` |

## Semantics

- The workspace must be prepared, halted, or stopped.
- Only regular files are supported.
- Workspace paths must be absolute file paths.
- Paths containing spaces or tabs are rejected on both sides - the `debugfs`
  transport cannot carry them.
- Copying from a workspace to a host directory writes a file with the same
  basename as the workspace path.
- Attached disk names refer to the `--disk` or `--bundle` names recorded in
  the workspace manifest.
- The implementation uses `debugfs`; pass `--debugfs` when it is not on `PATH`.
- `cp` operates on offline disks; it takes no backend or supervisor selection.

## Exit status

`cp` exits `0` on success; nonzero when the workspace is running, an endpoint
is invalid, the source file is missing or not a regular file, or the `debugfs`
copy fails.

## Related

- [`create`](create.md) - attach disks and bundles at create time
- [`clone`](clone.md) - copy the whole workspace instead
- [`logs`](logs.md) - read serial output without copying files
