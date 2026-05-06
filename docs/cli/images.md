---
title: microagent images
description: List or prune local image records.
---

```text
microagent images list [--state-dir <dir>]
microagent images prune [--state-dir <dir>]
```

`images` reads Microagent's local image index. Successful workspace rootfs
builds record the source image reference, resolved digest, platform, rootfs
path, size, and last-used time.

## Commands

| Command | Description |
|---|---|
| `list` | Show locally recorded images |
| `prune` | Remove records whose rootfs path no longer exists |

`prune` updates only the image index. It does not delete active workspace
rootfs files.

## Examples

```bash
microagent images list
microagent images prune --json
```

## Related

- [`create`](/cli/create/)
- [`rootfs`](/cli/rootfs/)
