---
title: microagent images
description: List or prune local image records.
---

```text
microagent images pull <image> [--state-dir <dir>]
microagent images list [--state-dir <dir>]
microagent images tag <source> <target> [--state-dir <dir>]
microagent images rm <image> [--delete] [--state-dir <dir>]
microagent images prune [--delete] [--state-dir <dir>]
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
| `rm` | Remove a local image record, optionally deleting an unshared baseline |
| `prune` | Remove stale records, and optionally delete reusable local rootfs baselines |

By default, `prune` updates only the image index by removing records whose
rootfs path no longer exists. With `--delete`, it also deletes reusable rootfs
baselines under the local image store and removes every record pointing to
those files. It does not delete workspace-owned rootfs files.

`tag` resolves `<source>` against the recorded image reference, resolved
reference, or digest. It creates another index record pointing at the same
local rootfs path.

`rm` resolves `<image>` the same way. With `--delete`, it deletes a reusable
image-store rootfs only when no remaining image record points to that file.

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

## Prune Flags

| Flag | Description |
|---|---|
| `--delete` | Delete reusable image-store rootfs files and their records |

## Remove Flags

| Flag | Description |
|---|---|
| `--delete` | Delete the reusable image-store rootfs when no kept record still uses it |

## Examples

```bash
microagent images pull docker.io/library/ubuntu:24.04
microagent images list
microagent images tag sha256:abc local/ubuntu:baseline
microagent images rm local/ubuntu:baseline
microagent create research --image local/ubuntu:baseline
microagent --json images prune
microagent --json images prune --delete
```

## Related

- [`create`](/cli/create/)
- [`rootfs`](/cli/rootfs/)
