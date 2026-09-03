---
title: microagent rootfs
description: Build an ext4 rootfs from an OCI image.
---

<!-- docs-last-updated -->
_Last updated: 2026-09-03_

```text
microagent rootfs build --image <ref> --out <path> [flags]   Build an ext4 rootfs from an OCI image
```

`rootfs build` pulls an OCI image and writes an ext4 disk image. Use it when
you want to prepare a rootfs ahead of time or hand it to a workspace via
`create --rootfs`; the normal `run`/`create` paths build the same rootfs for
you.

By default, `rootfs build` only accepts images pinned by digest. Pass
`--allow-mutable` to accept tag references - [`run`](run.md) and
[`create`](create.md) accept both; this is the stricter path. See
[security](../security.md) for the rationale.

Layer extraction is bounded before files are written to the host staging tree.
With `--size-mib`, cumulative expanded archive data cannot exceed that disk size.
Without it, automatic sizing accepts up to 32 GiB of expanded archive data; set an
explicit larger size when building a larger image. Archive entry count and path
depth are bounded separately. Image config metadata is limited to 4 MiB.

## The build-stage cache

Every build resolves the image's manifest digest from its source first —
so a tag always means what the registry currently says it means. The
extracted base image tree is then cached under
`~/.microagent/build/base-cache`, keyed by that digest. When the digest is
unchanged since a previous build, the layer download and extraction are
skipped and only the ext4 image is rebuilt. (Per-workspace config travels
on a separate boot-time config disk, never inside the image.) A moved tag,
by construction, misses the cache and fetches the new content.

The provenance envelope records which path a build took in `base_source`:
`registry` or `local-layout` when the content was fetched this build,
`cache` when a cached tree supplied it. Unusable cache entries are treated
as a miss and overwritten by the next successful build; the cache keeps
only the most recently used entries and can be cleared with
[`image prune --purge`](image.md).

Set `MICROAGENT_ROOTFS_BASE_CACHE_DIR` to relocate the cache, or set it to
an empty value to disable caching for a run.

## Examples

Build a rootfs from a digest-pinned image:

```bash
microagent rootfs build \
  --image docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6 \
  --size-mib 64 \
  --out /tmp/busybox-rootfs.ext4
```

Hand the result to `create`:

```bash
microagent create \
  --id agent-1 \
  --kernel /tmp/kernel \
  --rootfs /tmp/busybox-rootfs.ext4 \
  --state-dir /tmp/microagent
```

## `build` flags

Common flags:

- `--image <ref>` and `--out <path>` - the required pair: what to build from and
  where the ext4 image lands
- `--size-mib <MiB>` - pin the disk size and extraction budget; an image that
  doesn't fit then fails the build. Without the flag the disk grows to fit the
  image, up to the automatic 32 GiB extraction limit
- `--arch <arch>` - cross-build for a guest architecture other than the host's
  (the default)
- `--allow-mutable` - accept a tag reference when you've decided digest pinning
  isn't worth it for this build
- `--keep-stage` - keep the unpacked stage directory to debug what went
  into the image

The complete set:

| Flag | Description |
|---|---|
| `--image <ref>` | OCI image reference |
| `--out <path>` | Output rootfs path |
| `--os <os>` | Target OS (default `linux`) |
| `--arch <arch>` | Target architecture (`arm64`/`aarch64`, `amd64`/`x86_64`). Defaults to the host architecture |
| `--size-mib <MiB>` | Disk size |
| `--mke2fs <path>` | mke2fs binary path. Defaults to `mke2fs` from `PATH`, then the keg-only Homebrew e2fsprogs location on macOS |
| `--debugfs <path>` | debugfs binary path used to apply OCI inode metadata to the offline ext4 image. Resolved alongside `mke2fs` |
| `--exec <command>` | Shell command to run as guest init |
| `--init <path>` | Guest init path to inject |
| `--state-dir <dir>` | Builder state directory |
| `--keep-stage` | Keep the temporary unpacked stage directory |
| `--stage-snapshot <path>` | Copy the unpacked stage directory to this path before ext4 creation |
| `--allow-mutable` | Allow tag references (image without a digest) |

See [global flags](index.md#global-flags) for `--output`/`--json`.

For private registries, microagent resolves credentials without any Docker
dependency: from `$REGISTRY_AUTH_FILE` (the convention shared with
Podman/Skopeo/Buildah) or `~/.microagent/auth.json` (written by
[`microagent registry login`](registry.md)). Credential helpers are never
executed, Docker's `~/.docker/config.json` is never read, and public images
always pull anonymously. See [registry](registry.md) for the resolution
order.

## OCI metadata and defaults

Layer extraction preserves ownership, complete permission and special-mode
bits, mtimes, hard links, symlinks, extended attributes (including Linux file
capabilities), character/block devices, and FIFOs. Socket entries are rejected:
a persistent socket inode is not meaningful in a stopped root filesystem.

The image record also preserves `User`, `Env`, `Entrypoint`, `Cmd`,
`WorkingDir`, `StopSignal`, exposed ports, volumes, and labels. Normal
`run`/`create` workload boots honor the user, working directory, stop signal,
environment, and command defaults. Setup and maintenance phases stay
privileged. Exposed ports and volumes are declarations only; publish ports and
attach disks explicitly.

## Exit status

`rootfs build` exits `0` when the rootfs is written; nonzero when the image
cannot be pulled, the reference is a mutable tag without `--allow-mutable`, or
the ext4 image cannot be created.

## Related

- [`create`](create.md) - consume the rootfs with `--rootfs`
- [`run`](run.md) - the one-shot path that builds this for you
- [`image`](image.md) - reusable cached rootfs baselines
