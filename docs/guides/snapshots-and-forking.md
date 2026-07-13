---
title: Snapshot and fork workspaces
description: Checkpoint a running workspace, restore it in place, or fork copies from one snapshot.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-13_

A snapshot freezes a workspace - guest memory, device state, and disk - at
one moment. Use it to roll back a workspace in place, or fork independent
copies that resume from the same checkpoint. If the host cannot save and restore VM memory,
`snapshot create` fails with a structured error before writing a partial
checkpoint.

## 1. Get a workspace into a state worth keeping

```bash
microagent create builder --image docker.io/library/alpine:3.20
microagent start builder
microagent exec builder -- sh -c "echo checkpoint-1 > /root/progress.txt"
```

## 2. Take the snapshot

```bash
microagent snapshot create builder --tag baseline
```

```text
snapshot baseline created (512 MiB RAM, 2 vCPU) at 2026-06-11T08:52:44Z
```

A running workspace is briefly auto-paused, snapshotted, and resumed; an
already-paused one is snapshotted in place and left paused. `--tag` defaults
to a timestamp, and one workspace can hold many tags:

```bash
microagent snapshot list builder
```

```text
TAG                      SIZE         CREATED               IMAGE
baseline                 1.5GiB       2026-06-11T08:52:44Z  docker.io/library/alpine:3.20
```

Each snapshot stores the VM state plus a full rootfs copy, so size is roughly
saved guest state plus the disk. `snapshot delete` reclaims the space.

## 3. Restore in place

`start --from-snapshot` rolls the same workspace back: memory, devices, and
disk return to the snapshot point, and everything since is discarded.

```bash
microagent halt builder
microagent start builder --from-snapshot baseline
microagent exec builder -- cat /root/progress.txt
```

```text
checkpoint-1
```

A common use: snapshot before a risky upgrade, restore if it goes wrong.

## 4. Fork new workspaces from it

`create --from-snapshot <workspace>:<tag>` makes a *new* workspace from the
same moment - fresh identity, private copy of the rootfs, resumed from the
snapshot's memory:

```bash
microagent create builder-fork --from-snapshot builder:baseline
microagent exec builder-fork -- sh -c "echo fork-only > /root/progress.txt"
microagent exec builder-fork -- cat /root/progress.txt
microagent exec builder -- cat /root/progress.txt
```

```text
fork-only
checkpoint-1
```

Changes in a fork don't affect the source workspace. You can create any
number of forks from one tag without repeating boot and setup. Networked forks
use `user` mode (the default): each fork gets its own per-VM network namespace,
so any number run concurrently.

## 5. Know the contract

- **Same kernel.** The manifest records the kernel sha256; restore and fork
  refuse a different kernel.
- **Connections reset.** Host networking is re-established fresh on restore
  and fork, so in-flight TCP and vsock sessions (exec, shell,
  [mediation](/guides/agents-and-mediation/)) do not survive - the guest
  process is expected to reconnect.
- **Secrets are scrubbed.** Workspaces with delivered secrets get `/run/secrets`
  purged before the memory file is written and rehydrated on resume, restore,
  and fork - see [deliver secrets](/guides/secrets/).

## Clean up

```bash
microagent halt builder-fork && microagent delete builder-fork --yes
microagent halt builder
microagent snapshot delete builder baseline
microagent delete builder --yes
```

Deleting a workspace also removes its snapshots; `snapshot delete` removes a
single tag.

## Related

- [Keep a persistent workspace](/guides/persistent-workspaces/) — the lifecycle of the workspaces you're snapshotting.
- [Build agents on the mediation channel](/guides/agents-and-mediation/) — why mediation sessions reset on restore and fork.
- [`snapshot`](/cli/snapshot/) and [`create`](/cli/create/) — the full snapshot and fork flags.
- [`pause`](/cli/pause/) / [`resume`](/cli/resume/) — pause without a disk artifact.
