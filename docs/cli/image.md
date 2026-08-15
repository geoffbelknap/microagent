---
title: microagent image
description: Pull, list, tag, push, and prune local image records.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-15_

```text
microagent image pull <image> [--state-dir <dir>]                       Pull and record an image
microagent image list [--state-dir <dir>]                               List local image records
microagent image push <image> [--state-dir <dir>]                       Push a committed image
microagent image tag <source> <target> [--state-dir <dir>]              Tag an image record
microagent image delete <image> [--purge] [--yes] [--state-dir <dir>]   Remove an image record
microagent image prune [--purge] [--yes] [--state-dir <dir>]            Prune stale image records
```

`image` reads the local image index. Successful workspace rootfs
builds and `image pull` record the source image reference, resolved digest,
platform, rootfs path, size, last-used time, and rootfs SHA-256. Before an
image-store rootfs is published, microagent measures it and removes its host
write bits. Workspaces receive private writable copies; the shared baseline is
never attached to a guest.

Human `pull` output reports manifest and layer retrieval, rootfs conversion,
verification, and baseline publication. Layer byte totals are used when the
registry provides them; a reusable base-cache hit is identified without a
download claim. `push` reports completed OCI artifacts and registry
publication. Progress goes to stderr and does not alter JSON or MCP results.
`image prune` uses a bounded item counter while it reconciles the local index;
fast, empty cache scans remain quiet.

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
      "rootfs_sha256": "def...",
      "rootfs_immutable": true,
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

`delete --purge` and `prune --purge` also clear the build-stage cache — the
extracted base image trees that rootfs builds keep under
`~/.microagent/build/base-cache` so repeat builds of an unchanged image skip
the download. `delete` clears only the purged image's entries (unless another
kept record still names the same digest); `prune` clears them all. The result
reports the entries and bytes reclaimed. The cache is bounded and rebuilt on
demand, so clearing it costs nothing but the next pull.

`tag` resolves `<source>` against the recorded image reference, resolved
reference, or digest. It creates another index record pointing at the same
local rootfs path.

`delete` resolves `<image>` the same way. With `--purge`, it asks for confirmation
and deletes a reusable image-store rootfs only when no remaining image record
points to that file.

`create` and `run` reuse a recorded baseline whenever one exists for the
image with a matching guest init, stripped-setuid policy, SHA-256 identity,
and read-only host posture. Older cache entries rebuild once instead of being
trusted as immutable bases. Commands, env, declared files, disks,
published ports, shells, and hostnames all reach the guest at boot time,
through the per-boot config disk and kernel command line. None of them
change the rootfs bytes. Only an explicitly requested disk size forces a
rebuild (baselines are built at the default size), and the first build of
any image records a baseline automatically, so the speedup needs no
explicit `image pull`.

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
| `--debugfs <path>` | debugfs binary path used to apply OCI filesystem metadata |
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
non-interactive input cannot provide.

## Related

- [`create`](/cli/create/) - build a workspace from a pulled image
- [`rootfs`](/cli/rootfs/) - the lower-level rootfs build path
