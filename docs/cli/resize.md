---
title: microagent resize
description: Grow or shrink a stopped workspace's rootfs disk in place.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-15_

```text
microagent resize <workspace> --size-mib <n> [options]
```

`resize` grows or shrinks a stopped workspace's rootfs disk in place. The
workspace must be halted or stopped, and must have no snapshots - a
snapshot's machine state captures device geometry, and a restore would
replace the resized disk with the snapshot's own anyway. Growing extends the
disk immediately; shrinking refuses when the target is smaller than the
filesystem's own reported usage. For a named volume's disk, use
[`volume resize`](volume.md) instead.

Both directions run entirely on the host, offline: the rootfs's ext4
filesystem is grown or shrunk with `resize2fs`, and a shrink runs `e2fsck -f`
first (`resize2fs`'s own precondition). Nothing in the guest needs to be
aware a resize happened; the new size takes effect the next time the
workspace starts.

In a terminal, resize reports the validation, filesystem check, disk,
filesystem, verification, and publication phases on stderr when the operation
takes long enough to notice. Publication is reported only after the workspace
manifest records the verified size. JSON and MCP responses contain no terminal
progress text.

## Examples

Grow a workspace's disk before installing something large:

```bash
microagent halt research
microagent resize research --size-mib 16384
microagent start research
```

Check the result:

```bash
microagent --json resize research --size-mib 16384
```

```json
{
  "workspace": "research",
  "from_size_mib": 8192,
  "to_size_mib": 16384,
  "usage": {
    "sizeMiB": 16384,
    "fsUsedMiB": 912,
    "fsFreeMiB": 15472,
    "hostAllocatedMiB": 950,
    "usedPercent": 6
  }
}
```

## Flags

| Flag | Description |
|---|---|
| `--size-mib <n>` | Target rootfs size in MiB |
| `--resize2fs <path>` | `resize2fs` binary path |
| `--backend <name>` | Backend identity override |
| `--state-dir <dir>` | State directory holding the workspace (default `~/.microagent/`) |

See [global flags](index.md#global-flags) for `--output`/`--json`.

## Exit status

`resize` exits `0` on success. It exits nonzero when the workspace cannot be
found, is running, starting, paused, or quarantined, has one or more
snapshots, or when a shrink target is smaller than the filesystem's own
reported usage.

## Related

- [`volume`](volume.md) - `volume resize` for a named volume's disk
- [`status`](status.md) - `inspect`/`status` report the disk's provisioned,
  filesystem-used, and host-allocated size
- [`snapshot`](snapshot.md) - delete snapshots before resizing the rootfs
