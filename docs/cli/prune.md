---
title: microagent prune
description: Prune stale local records and optional reusable image baselines.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

```text
microagent prune [--images] [--yes] [--state-dir <dir>]
```

`prune` cleans local cache metadata. By default it removes stale image records
whose rootfs files no longer exist. It does not delete workspace disks.

Use `--images` to also delete reusable image-store rootfs baselines under the
local image store. That path asks for confirmation unless `--yes` is passed.
This is the same cleanup as [`images prune`](/cli/images/), where the
baseline-deletion flag is spelled `--delete` instead of `--images` - either
command works for this job.

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

## Flags

You'll rarely need flags here beyond `--images` - the default run only removes
records whose files are already gone.

| Flag | Description |
|---|---|
| `--images` | Delete reusable image-store rootfs files and their records |
| `--yes`, `-y` | Confirm image deletion without prompting |
| `--state-dir <dir>` | State directory holding local records (default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Exit status

`prune` exits `0` on success; nonzero when a deletion needs confirmation that
non-interactive input cannot provide, or when a file cannot be removed. In AX
mode a failure is written as a structured error envelope.

## Related

- [`images`](/cli/images/) - `images prune` is the same cleanup
- [`delete`](/cli/delete/) - remove a workspace and its disks
