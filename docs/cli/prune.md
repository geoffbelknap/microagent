---
title: microagent prune
description: Prune stale local records and optional reusable image baselines.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-10_

```text
microagent prune [--images] [--yes] [--state-dir <dir>]
```

`prune` cleans local cache metadata. By default it removes stale image records
whose rootfs files no longer exist. It does not delete workspace disks.

Use `--images` to also delete reusable image-store rootfs baselines under the
local image store. That path asks for confirmation unless `--yes` is passed.

## Flags

| Flag | Description |
|---|---|
| `--images` | Delete reusable image-store rootfs files and their records |
| `--yes`, `-y` | Confirm image deletion without prompting |
| `--state-dir <dir>` | State directory holding local records |

## Examples

Remove stale image records:

```bash
microagent prune
```

Delete reusable image baselines too:

```bash
microagent prune --images
```

Non-interactive cleanup:

```bash
microagent prune --images --yes
```

## Related

- [`images`](/cli/images/)
- [`delete`](/cli/delete/)
