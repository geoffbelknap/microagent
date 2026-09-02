---
title: Use volumes and move data
description: Persist data in named volumes, attach disks and bundles, and copy files with cp.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-13_

microagent never exposes host directories to the guest - everything the guest
reads or writes is a block device - so data moves through deliberate, declared
paths. Use named volumes for data that outlives a workspace, attach existing
disks and tar bundles directly, and copy single files in or out with `cp`.

## 1. Create a named volume

A named volume is a platform-managed ext4 disk with a lifecycle independent of
any one workspace - the in-boundary analog of a container volume.

```bash
microagent volume create data --size-mib 512
microagent volume list
```

```text
Created volume "data" (512 MiB)
NAME                 SIZE-MIB   ATTACHED
data                 512        -
```

## 2. Attach it by name

Pass `--volume <name>:/mount` (or `-v`) on `run` or `create`. A bare name
resolves to a managed volume; the data persists after the workspace is gone.

```bash
microagent run -v data:/work docker.io/library/alpine:3.20 sh -c "echo 42 > /work/counter.txt"
microagent run -v data:/work docker.io/library/alpine:3.20 cat /work/counter.txt
```

```text
42
```

Two one-shot runs, one surviving file. `volume status` shows the backing disk
and the last holder:

```bash
microagent volume status data
```

```text
Name:     data
Size:     512 MiB
Created:  2026-06-11T08:51:35Z
Attached: run-golden-marmot-2c9d
Path:     /home/you/.microagent/volumes/data.ext4
```

A volume is single-attach: one running workspace holds it at a time, so two
microVMs never mount the same ext4 read-write. Attaching to a volume held by a
running workspace fails closed; a holder that is no longer running is
reclaimed automatically.

## 3. Attach disks and bundles

When the data already lives in a file on the host, skip the registry and
attach it directly:

- `--disk name=/path.ext4:/mount:rw` attaches an existing ext4 image you
  manage yourself.
- `--bundle name=/path.tar:/mount:ro` builds a one-shot ext4 disk from a tar
  archive at start - the portable way to get a directory's contents in.

```bash
mkdir -p ./config-dir
echo 'bundled file' > ./config-dir/hello.txt
tar -C ./config-dir -cf /tmp/config.tar .
microagent run --bundle config=/tmp/config.tar:/config:ro \
  --image docker.io/library/alpine:3.20 --exec "cat /config/hello.txt"
```

```text
bundled file
```

The container-style `-v` alias accepts all three source forms: a bare name is
a managed volume, `.tar`/`.tar.gz`/`.tgz` is a bundle, `.ext4`/`.img` is a
disk. Host directory bind mounts are deliberately not one of them.

## 4. Copy single files with cp

`cp` moves one regular file between the host and a workspace disk while the
workspace is not running - no boot required.

```bash
echo 'config-v1' > ./app.conf
microagent create scratch --image docker.io/library/alpine:3.20 -v data:/work
microagent cp ./app.conf scratch:/etc/app.conf
microagent start scratch
microagent exec scratch -- cat /etc/app.conf /work/counter.txt
```

```text
config-v1
42
```

Copy back out after halting:

```bash
microagent halt scratch
microagent cp scratch:/etc/app.conf ./app.conf.out
```

Address an attached disk by its name: `scratch:data:/counter.txt`. For guest
paths you declared up front, [`--output` artifacts](one-shot-runs.md)
are the more structured egress path.

## 5. Pick the right tool

| You want | Use |
|---|---|
| Data that outlives any workspace, reattached by name | named volume |
| An ext4 image you build or manage yourself | `--disk` |
| A host directory's contents, read in at start | `--bundle` |
| One file in or out of a stopped workspace | `cp` |
| Declared result files out of a run | `--output` |

## Clean up

```bash
microagent delete scratch --yes
microagent volume delete data
```

`volume delete` fails closed while the volume is attached to a running workspace;
`--force` overrides. Deleting a workspace releases its volumes - the data
stays for the next attach, until `volume delete` removes the backing disk.

## Related

- [Storage](../concepts/storage.md) — the storage model behind volumes, disks, and bundles.
- [`volume`](../cli/volume.md) — volume flags and semantics.
- [Run a service](run-a-service.md) — Postgres on a named volume, end to end.
