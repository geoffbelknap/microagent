---
title: microagent images
description: List or prune local image records.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-02_

```text
microagent images pull <image> [--state-dir <dir>]
microagent images list [--state-dir <dir>]
microagent images push <image> [--state-dir <dir>]
microagent images tag <source> <target> [--state-dir <dir>]
microagent images rm <image> [--delete] [--yes] [--state-dir <dir>]
microagent images prune [--delete] [--yes] [--state-dir <dir>]
```

`images` reads the local image index. Successful workspace rootfs
builds and `images pull` record the source image reference, resolved digest,
platform, rootfs path, size, and last-used time.

## Commands

| Command | Description |
|---|---|
| `pull` | Build and record a reusable local rootfs from an OCI image |
| `list` | Show locally recorded images |
| `push` | Push a [committed](/cli/commit/) image from the local OCI layout to its registry |
| `tag` | Add another local name for an existing image record |
| `rm` | Remove a local image record, optionally deleting an unshared baseline |
| `prune` | Remove stale records, and optionally delete reusable local rootfs baselines |

By default, `prune` updates only the image index by removing records whose
rootfs path no longer exists. With `--delete`, it also deletes reusable rootfs
baselines under the local image store and removes every record pointing to
those files after confirmation. It does not delete workspace-owned rootfs files.

`tag` resolves `<source>` against the recorded image reference, resolved
reference, or digest. It creates another index record pointing at the same
local rootfs path.

`rm` resolves `<image>` the same way. With `--delete`, it asks for confirmation
and deletes a reusable image-store rootfs only when no remaining image record
points to that file.

For clean workspace baselines, `create` reuses a pulled or tagged image record
when the workspace has no setup commands, entrypoint, env overrides, or
attached disks. Workspaces that need guest config are rebuilt from the source
OCI image so their init config is baked into the rootfs.

For private registries, image pulls read standard registry credential
configuration from `$DOCKER_CONFIG/config.json` or `~/.docker/config.json`,
including configured credential helpers. MicroAgent uses those credentials for
pulls and does not write registry login state.

## Pull flags

| Flag | Description |
|---|---|
| `--arch <arch>` | Target architecture |
| `--size-mib <MiB>` | Rootfs image size |
| `--mke2fs <path>` | mke2fs binary path |
| `--guest-init <path>` | Guest init binary path |

## Prune flags

| Flag | Description |
|---|---|
| `--delete` | Delete reusable image-store rootfs files and their records |
| `--yes`, `-y` | Confirm deletion without prompting |

## Remove flags

| Flag | Description |
|---|---|
| `--delete` | Delete the reusable image-store rootfs when no kept record still uses it |
| `--yes`, `-y` | Confirm deletion without prompting |

## Examples

```bash
microagent images pull docker.io/library/ubuntu:24.04
microagent images list
microagent images tag sha256:abc local/ubuntu:baseline
microagent images rm local/ubuntu:baseline
microagent create research --image local/ubuntu:baseline
microagent --json images prune
microagent --json images prune --delete --yes
```

`images list` prints one row per recorded image:

```text
IMAGE                                            DIGEST                       PLATFORM         SIZE       LAST USED
docker.io/library/ubuntu:24.04                   sha256:abc...                linux/amd64      268435456  2026-06-01T12:00:00Z
```

With the global `--json` flag, the records are returned under `images`:

```json
{
  "images": [
    {
      "image_ref": "docker.io/library/ubuntu:24.04",
      "resolved_ref": "docker.io/library/ubuntu@sha256:abc...",
      "digest": "sha256:abc...",
      "platform": { "os": "linux", "architecture": "amd64" },
      "output_path": "/home/user/.microagent/images/sha256-abc.../rootfs.ext4",
      "size_bytes": 268435456,
      "last_used_at": "2026-06-01T12:00:00Z"
    }
  ]
}
```

## Related

- [`create`](/cli/create/)
- [`rootfs`](/cli/rootfs/)
