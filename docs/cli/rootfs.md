---
title: microagent rootfs
description: Build an ext4 rootfs from an OCI image.
---

```text
microagent rootfs build --image <ref> --out <path> [flags]
```

`rootfs build` pulls an OCI image and writes an ext4 disk image. Use it when
you want to prepare a rootfs ahead of time or hand it to a workspace via
`--rootfs`.

## Subcommands

- `build` — build a rootfs from an OCI image

## `build` flags

| Flag | Description |
|---|---|
| `--image <ref>` | OCI image reference |
| `--out <path>` | Output rootfs path |
| `--os <os>` | Target OS (default `linux`) |
| `--arch <arch>` | Target architecture (`amd64`, `arm64`) |
| `--size-mib <MiB>` | Disk size |
| `--mke2fs <path>` | mke2fs binary path |
| `--exec <command>` | Shell command to run as guest init |
| `--allow-mutable` | Allow tag references (image without a digest) |

By default, `rootfs build` only accepts images pinned by digest. Pass
`--allow-mutable` to accept tag references.

For private registries, MicroAgent reads Docker-compatible credential
configuration from `$DOCKER_CONFIG/config.json` or `~/.docker/config.json`,
including configured credential helpers. It does not store registry credentials.

## Example

```bash
microagent rootfs build \
  --image docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6 \
  --arch arm64 \
  --size-mib 64 \
  --mke2fs /opt/homebrew/opt/e2fsprogs/sbin/mke2fs \
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

## Related

- [`create`](/cli/create/), [`run`](/cli/run/)
