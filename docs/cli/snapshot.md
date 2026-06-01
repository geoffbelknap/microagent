---
title: microagent snapshot
description: Create, list, and remove memory-plus-disk workspace snapshots.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

```text
microagent snapshot create <name> [--tag <tag>] [--state-dir <dir>]
microagent snapshot list <name> [--state-dir <dir>]
microagent snapshot rm <name> <tag> [--state-dir <dir>]
```

A snapshot is a full checkpoint of a workspace: its guest memory and device
state plus a coherent copy of its rootfs disk, taken together while the VM is
paused. Snapshots are Firecracker-only. They are stored under
`<state-dir>/<name>/snapshots/<tag>/` as `vmstate`, `memory`, `rootfs.ext4`,
and `manifest.json`.

A workspace may hold multiple named snapshots; `--tag` defaults to a timestamp.

## create

`snapshot create` checkpoints a running or paused workspace. Firecracker
requires the VM be paused before a snapshot is written, so a running workspace
is briefly auto-paused, snapshotted, and resumed (the pause appears in the
event history as `running → paused → running`). An already-paused workspace is
snapshotted in place and left paused.

The manifest records the image reference, network mode, the guest IP to
re-establish on restore, the kernel sha256 (used to reject loading against a
different kernel), the vCPU/memory sizing, and the creation time.

Because each snapshot stores both a memory file and a full rootfs copy, total
size is roughly the touched guest RAM plus the rootfs size. `snapshot list`
reports each tag's size; `snapshot rm` and `delete <name>` reclaim the space.

## list

`snapshot list` reports the snapshots recorded for a workspace with each tag's
on-disk size, creation time, and source image. It is a host-side read and works
whether or not the workspace is running.

## rm

`snapshot rm` deletes a single snapshot tag. It is a host-side operation and
does not require a running workspace.

## Connection-reset contract

Snapshots are restored with [`start --from-snapshot`](/cli/start/)
(resume-in-place) and forked with [`create --from-snapshot`](/cli/create/). On
restore the host networking is re-established fresh, so in-flight guest
connections — outbound TCP and live vsock sessions (exec/shell/mediation) — do
not survive; the guest body is expected to reconnect. Bridged networking is not
supported for snapshot or fork.

## Flags

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name; positional name is also accepted |
| `--id <id>` | Workspace ID alias for `--name` |
| `--tag <tag>` | Snapshot tag for `create` (defaults to a timestamp) |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override (`create`) |
| `--supervisor <path>` | Override the installed host backend supervisor path (`create`) |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`/`--supervisor`.

## Example

```bash
microagent snapshot create research --tag pre-upgrade
microagent snapshot list research
microagent start research --from-snapshot pre-upgrade
microagent snapshot rm research pre-upgrade
```

## Related

- [`start`](/cli/start/), [`create`](/cli/create/), [`pause`](/cli/pause/), [`delete`](/cli/delete/)
