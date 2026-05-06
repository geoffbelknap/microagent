---
title: microagent images
description: List or prune local image records.
---

```text
microagent images pull <image> [--state-dir <dir>]
microagent images list [--state-dir <dir>]
microagent images tag <source> <target> [--state-dir <dir>]
microagent images prune [--state-dir <dir>]
```

`images` reads Microagent's local image index. Successful workspace rootfs
builds and `images pull` record the source image reference, resolved digest,
platform, rootfs path, size, and last-used time.

## Commands

| Command | Description |
|---|---|
| `pull` | Build and record a reusable local rootfs from an OCI image |
| `list` | Show locally recorded images |
| `tag` | Add another local name for an existing image record |
| `prune` | Remove records whose rootfs path no longer exists |

`prune` updates only the image index. It does not delete active workspace
rootfs files.

`tag` resolves `<source>` against the recorded image reference, resolved
reference, or digest. It creates another index record pointing at the same
local rootfs path.

For clean workspace baselines, `create` reuses a pulled or tagged image record
when the workspace has no setup commands, entrypoint, env overrides, or
attached disks. Workspaces that need guest config are rebuilt from the source
OCI image so their init config is baked into the rootfs.

## Pull Flags

| Flag | Description |
|---|---|
| `--arch <arch>` | Target architecture |
| `--size-mib <MiB>` | Rootfs image size |
| `--mke2fs <path>` | mke2fs binary path |
| `--guest-init <path>` | Guest init binary path |

## Examples

```bash
microagent images pull docker.io/library/ubuntu:24.04
microagent images list
microagent images tag sha256:abc local/ubuntu:baseline
microagent create research --image local/ubuntu:baseline
microagent images prune --json
```

## Related

- [`create`](/cli/create/)
- [`rootfs`](/cli/rootfs/)
