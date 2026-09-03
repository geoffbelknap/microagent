---
title: Storage
description: Choose between the rootfs, attached disks, tar bundles, and named volumes.
---

<!-- docs-last-updated -->
_Last updated: 2026-09-03_

A workspace sees block devices, never host directories. microagent does not
expose host bind mounts - everything the guest reads or writes is an ext4
disk image or the rootfs - which keeps the host filesystem outside the
workspace boundary by construction. This page maps every way data gets into,
out of, and between workspaces, and which to pick. For the hands-on
walkthrough of these mechanisms, see
[Use volumes and move data](../guides/volumes-and-data.md).

## The rootfs

Every workspace boots from a rootfs: an ext4 image built from an OCI image
(`run`/`create`) or an existing rootfs path. It is per-workspace and lives under
`<state-dir>/workspaces/<name>/rootfs.ext4`. Writes inside the guest persist in
that image across stop/start, and are discarded by `delete` (a one-shot `run`
discards them by default; `--rm` spells that out, `--keep` opts out).
[`commit`](../cli/commit.md) snapshots a stopped rootfs back into an OCI image.

When the image store has a reusable rootfs, microagent measures and seals that
shared file read-only. A workspace receives a private writable reflink or copy,
so package installs and other guest writes never modify the shared OS base.
JSON create and status output names the immutable source under `rootfs_base`
or `rootfsBase`; the workspace path still names the writable derived disk.

## Attaching extra storage

Beyond the rootfs, a workspace can attach additional disks at declared
mountpoints. There are three sources, all available through `-v`/`--volume`
(and the spec's `disks`/`bundles`):

| Source | Syntax | What it is | Lifecycle |
|---|---|---|---|
| ext4 image | `-v /path/data.ext4:/data` | An existing ext4 file attached as a disk | You own the file |
| tar bundle | `-v /path/ctx.tar:/ctx:ro` | A `.tar`/`.tar.gz`/`.tgz` built into a one-shot ext4 disk at start | Built per start |
| named volume | `-v data:/data` | A platform-managed ext4 disk addressed by name | Managed by microagent |

A bare name (no path separator or extension) is a **named volume**; a path
ending in `.tar`/`.tar.gz`/`.tgz` is a **bundle**; a path ending in
`.ext4`/`.img` is a raw **disk image**. Host directories are rejected with
guidance - package a directory as a tar for ingress, or use
[`cp`](../cli/cp.md) against a stopped workspace.

Bundle extraction accepts at most 32 GiB of expanded archive data and also bounds
archive entry count and path depth. Build a larger ext4 image yourself and
attach it as a disk when the source exceeds those limits.

## Named volumes

A [named volume](../cli/volume.md) is the in-boundary analog of a container volume:
a managed ext4 disk with a lifecycle independent of any one workspace. Instead
of hand-managing `.ext4` files, create one and attach it by name.

```bash
microagent volume create data --size-mib 2048
microagent run docker.io/library/python:3.12-slim --volume data:/work
```

Volumes live under `<state-dir>/volumes/` (an `index.json` registry plus one
disk per volume - bare ext4 as `<name>.ext4` on the Linux and macOS backends). A volume is **single-attach**: at most one running
workspace holds it at a time, so two VMs never mount the same ext4 read-write. A
holder that is no longer running is reclaimed automatically, and deleting a
workspace releases the volumes it held; the data persists for the next attach.

This is deliberately not the Docker volume model - there is no daemon, no volume
drivers, and no concurrent sharing between workspaces. It is a managed disk that
maps cleanly onto microVM semantics.

## Egress

To get data *out* of a workspace, declare [`--output`](../cli/artifact.md) paths or
use [`cp`](../cli/cp.md) against a stopped workspace. A named volume is also a
natural handoff: write results to it in one workspace, attach it to another.

## See also

- [Use volumes and move data](../guides/volumes-and-data.md) - the hands-on walkthrough
- [`microagent volume`](../cli/volume.md) - manage named volumes
- [`microagent.yaml`](../cli/spec.md) - declarative `disks` and `bundles`
- [`microagent cp`](../cli/cp.md) - stopped-workspace file transfer
- [Boundaries](boundaries.md) - why host directories stay outside
