---
title: microagent snapshot
description: Create, list, and remove memory-plus-disk workspace snapshots.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-15_

```text
microagent snapshot create <name> [--tag <tag>] [--forensic] [--state-dir <dir>]   Checkpoint a running workspace
microagent snapshot list <name> [--state-dir <dir>]                                List a workspace's snapshots
microagent snapshot delete <name> <tag> [--state-dir <dir>]                        Remove one snapshot
```

A snapshot is a full checkpoint of a workspace: guest memory, device state,
and a coherent copy of the rootfs disk, taken together while the VM is paused.
If the current host cannot save and restore VM memory, `snapshot create` fails
with a structured error before it writes a partial checkpoint. On macOS, save
and restore require a host session where Virtualization.framework permits
machine-state capture.

Snapshots are stored under `<state-dir>/<name>/snapshots/<tag>/`. Every
snapshot has `manifest.json`, saved VM state, and a coherent rootfs copy. A
workspace may hold multiple named snapshots. Without `--tag`, ordinary
snapshots use `snap-<timestamp>` and forensic snapshots use
`forensic-<timestamp>`.
Explicit tags start with a letter or digit and contain only letters, digits,
periods, underscores, or hyphens. A tag can contain at most 63 characters.

In a terminal, snapshot creation reports pause, secret handling, backend
capture, artifact verification, and publication phases on stderr when the
operation takes long enough to notice. A failed capture states whether the
source workspace resumed, remains paused, or could not be confirmed. JSON and
MCP responses contain no terminal progress text.

Three commands copy a workspace; pick by what you need to keep. `snapshot`
captures a live moment - memory included - so you can restore or fork
running state. [`commit`](/cli/commit/) turns a stopped workspace's disk into
an OCI image you can push and recreate from anywhere. [`clone`](/cli/clone/)
copies a stopped workspace's disks into a second workspace on the same host,
no image and no memory. If the in-memory state matters, snapshot; if you want
a distributable artifact, commit; for another local copy, clone.

## Examples

Checkpoint before a risky change, then roll back to it:

```bash
microagent snapshot create research --tag pre-upgrade
microagent snapshot list research
microagent halt research
microagent start research --from-snapshot pre-upgrade
```

Reclaim the space when the tag is no longer needed:

```bash
microagent snapshot delete research pre-upgrade
```

## `create`

`snapshot create` checkpoints a running or paused workspace. A running one is
briefly auto-paused, snapshotted, and resumed; an already-paused one is
snapshotted in place and left paused.

Memory comes from a live VM, so a workspace must have one. Ordinary snapshot
operations are fenced once containment is marked. `quarantine` owns the safe
exception: it freezes and severs first, takes its forensic memory-and-disk
capture while the VM stays frozen, then stops into custody.

### Forensic captures

`--forensic` captures for investigation rather than restore. The guest
secret purge is skipped, because credential material is the evidence and exists
only in volatile memory:

```sh
microagent snapshot create agent-1 --forensic --tag incident-4711
```

The command states two properties in its output:

- the artifact **retains guest secrets**. Its custody is yours from that point
  — put it somewhere the workloads it came from cannot read.
- it is **not restorable**. The manifest records secrets as materialized and
  un-purged, which `start --from-snapshot` and `create --from-snapshot` refuse.
  A capture can never be rehydrated into a running workspace.

The manifest also records `forensic: true` and
`frozenProcessState: true`. The latter means the backend machine-state artifact
preserves the process graph from paused vCPUs; it is not a process list supplied
by the guest after containment.

Also available over MCP as `snapshot.create` with `forensic: true` — `serve mcp`
is an operator surface, and an investigating operator works through it the same
way they work through the CLI.

## Manifest

The manifest records the image reference, network mode, guest address fields
needed on restore, and the kernel sha256 (used to reject loading against a
different kernel). It also records the vCPU/memory sizing, the creation time,
and the snapshot artifact paths.

Because each snapshot stores VM state plus a full rootfs copy, total size is
roughly saved state plus the rootfs size. `snapshot list` reports each tag's
size; `snapshot delete` and `delete <name>` reclaim the space.

## `list`

`snapshot list` reports the snapshots recorded for a workspace with each tag's
on-disk size, creation time, and source image. It is a host-side read and works
whether or not the workspace is running.

## `delete`

`snapshot delete` deletes a single snapshot tag. It is a host-side operation and
does not require a running workspace.

## Clock contract

A restored guest resumes with the wall clock it was captured with. After
`start --from-snapshot` brings the guest up, the host pushes its own time in
through the structured exec service, so guest time is correct to within a
second once the workspace is ready. The sync is best-effort: its outcome -
synced, or why not - is recorded in the workspace event history
(`microagent events`), and a guest that cannot be reached keeps its stale
clock rather than failing the start.

## Connection-reset contract

Snapshots are restored with [`start --from-snapshot`](/cli/start/)
(resume-in-place) and forked with [`create --from-snapshot`](/cli/create/). On
restore the host networking is re-established fresh, so in-flight guest
connections - outbound TCP and live vsock sessions (exec/shell/mediation) - do
not survive; the guest process is expected to reconnect. Halt the source before
restoring, and treat the window between a running-state snapshot and the next
restore as one where sessions need re-establishing.

## Flags

`--tag` names the checkpoint instead of getting a timestamp.

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name; positional name is also accepted |
| `--id <id>` | Workspace ID alias for `--name` |
| `--tag <tag>` | Snapshot tag for `create`; identifier up to 63 characters (defaults to `snap-<timestamp>` or `forensic-<timestamp>`) |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override (`create`) |
| `--supervisor <path>` | Override the installed host backend supervisor path (`create`) |

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--supervisor`.

## Exit status

`snapshot` subcommands exit `0` on success; nonzero when the workspace or tag
cannot be found, the workspace is not in a snapshottable state, or the
checkpoint cannot be written.

## Related

- [`start`](/cli/start/) - restore in place with `--from-snapshot`
- [`create`](/cli/create/) - fork a new workspace with `--from-snapshot`
- [`commit`](/cli/commit/) - a distributable OCI image instead of a checkpoint
- [`clone`](/cli/clone/) - a plain disk copy without memory
- [Snapshot and fork workspaces](/guides/snapshots-and-forking/) - the walkthrough
