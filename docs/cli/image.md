---
title: microagent image
description: Pull, list, tag, push, and prune local image records.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-23_

```text
microagent image pull <image> [--state-dir <dir>]                    Pull and record an image
microagent image list [--state-dir <dir>]                            List local image records
microagent image push <image> [--state-dir <dir>]                    Push a committed image
microagent image tag <source> <target> [--state-dir <dir>]           Tag an image record
microagent image delete <image> [--purge] [--yes] [--state-dir <dir>]   Remove an image record
microagent image prune [--purge] [--yes] [--state-dir <dir>]        Prune stale image records
```

`image` reads the local image index. Successful workspace rootfs
builds and `image pull` record the source image reference, resolved digest,
platform, rootfs path, size, and last-used time.

## Examples

Pull an image once, then reuse it for clean workspaces:

```bash
microagent image pull docker.io/library/ubuntu:24.04
microagent image list
microagent create research --image docker.io/library/ubuntu:24.04
```

Tag and remove local records:

```bash
microagent image tag sha256:abc local/ubuntu:baseline
microagent image delete local/ubuntu:baseline
```

Prune stale records, or also delete reusable rootfs baselines:

```bash
microagent --json image prune
microagent --json image prune --purge --yes
```

`image list` prints one row per recorded image. The DIGEST column shows a
short, 12-character form of the hash (the algorithm prefix is dropped), the
same convention `docker images` uses; the full digest is always available
with `--json` or `image inspect`:

```text
IMAGE                                            DIGEST                       PLATFORM         SIZE       LAST USED
docker.io/library/ubuntu:24.04                   abc123def456                 linux/amd64      268435456  2026-06-01T12:00:00Z
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

## Commands

| Command | Description |
|---|---|
| `pull` | Build and record a reusable local rootfs from an OCI image |
| `list` | Show locally recorded images |
| `push` | Push a [committed](/cli/commit/) image from the local OCI layout to its registry |
| `tag` | Add another local name for an existing image record |
| `delete` | Remove a local image record, optionally deleting an unshared baseline |
| `prune` | Remove stale records, and optionally delete reusable local rootfs baselines |

By default, `prune` updates only the image index by removing records whose
rootfs path no longer exists. With `--purge`, it also deletes reusable rootfs
baselines under the local image store and removes every record pointing to
those files after confirmation. It does not delete workspace-owned rootfs files.
`tag` resolves `<source>` against the recorded image reference, resolved
reference, or digest. It creates another index record pointing at the same
local rootfs path.

`delete` resolves `<image>` the same way. With `--purge`, it asks for confirmation
and deletes a reusable image-store rootfs only when no remaining image record
points to that file.

For clean workspace baselines, `create` reuses a pulled or tagged image record
only when the workspace needs no guest configuration at all: no setup commands
or entrypoint, no env overrides, no custom shell or hostname, no injected
files, no attached disks, and no published ports. Workspaces that need guest
config are rebuilt from the source OCI image so their init config is baked
into the rootfs.

For private registries, image pulls resolve credentials without any Docker
dependency: from `$REGISTRY_AUTH_FILE` (the convention shared with
Podman/Skopeo/Buildah) or `~/.microagent/auth.json` (written by
[`microagent registry login`](/cli/registry/)). Credential helpers are never
executed, and public images always pull anonymously. microagent does not write
Docker's login state. See [registry](/cli/registry/) for details.

## Flags

Common flags:

- `--purge` (`delete`/`prune`) - actually delete rootfs baselines, not just records
- `--yes` / `-y` (`delete`/`prune`) - skip the confirmation prompt in scripts
- `--arch <arch>` (`pull`) - pull for a non-default guest architecture
- `--size-mib <MiB>` (`pull`) - size the built rootfs

### Pull flags

| Flag | Description |
|---|---|
| `--arch <arch>` | Target architecture (`arm64`/`aarch64`, `amd64`/`x86_64`) |
| `--size-mib <MiB>` | Rootfs image size (default: fits the image) |
| `--mke2fs <path>` | mke2fs binary path |
| `--guest-init <path>` | Guest init binary path |

### Prune flags

| Flag | Description |
|---|---|
| `--purge` | Delete reusable image-store rootfs files and their records |
| `--yes`, `-y` | Confirm deletion without prompting |

### Delete flags

| Flag | Description |
|---|---|
| `--purge` | Delete the reusable image-store rootfs when no kept record still uses it |
| `--yes`, `-y` | Confirm deletion without prompting |

## Exit status

`image` subcommands exit `0` on success; nonzero when an image reference
cannot be resolved, a pull or push fails, or a deletion needs confirmation that
non-interactive input cannot provide. In AX mode a failure is written as a
structured error envelope.

## Related

- [`create`](/cli/create/) - build a workspace from a pulled image
- [`rootfs`](/cli/rootfs/) - the lower-level rootfs build path
