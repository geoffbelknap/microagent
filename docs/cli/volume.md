---
title: microagent volume
description: Manage user-defined named volumes - VM-independent ext4 disks attached by name.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-30_

```text
microagent volume create <name> [--size-mib <n>]   Create a named volume
microagent volume list                             List named volumes
microagent volume status <name>                    Show one volume
microagent volume delete <name> [--force]          Remove a named volume
```

A named volume is a platform-managed ext4 disk with a lifecycle independent of
any one workspace. It is the in-boundary analog of a container volume: instead
of hand-managing `.ext4` files and passing them with `--disk`, you create a
volume once and attach it by name. The registry and backing disks live under
`<state-dir>/volumes/` (`index.json` plus one `<name>.ext4` per volume).

## Examples

Create, show, and delete a volume:

```bash
microagent volume create data --size-mib 2048
microagent volume list
microagent volume status data
microagent volume delete data
```

Get the structured record:

```bash
microagent --json volume status data
```

```json
{
  "name": "data",
  "size_mib": 2048,
  "created_at": "2026-06-02T00:00:00Z",
  "attached_to": "research"
}
```

## Attaching by name

Attach a volume to a workspace with `--volume <name>:/mount[:ro|rw]`. A bare
name (lowercase letters, digits, and hyphens - no path separator or extension)
resolves to a managed volume. A path ending in `.tar`/`.tar.gz`/`.tgz` is still
treated as a bundle and `.ext4`/`.img` as a raw disk image.

```bash
microagent run --volume data:/work docker.io/library/alpine:3.21 touch /work/hello
microagent create research --image docker.io/library/alpine:3.21 --volume data:/work
```

A volume is **single-attach**: at most one running workspace holds it at a time,
so two VMs never mount the same ext4 read-write. Attaching to a volume already
held by a running workspace fails closed. A holder that is no longer running is
reclaimed automatically, so a crashed workspace never wedges a volume. Deleting
a workspace releases the volumes it held; the data persists for the next attach.

This is deliberately not the Docker volume model - there is no daemon, no volume
drivers, and no concurrent sharing between workspaces.

`volume delete` fails closed while the volume is attached to a running workspace;
pass `--force` to remove it and its backing disk anyway.

## Flags

`--size-mib` applies on create, `--force` on cleanup.

| Flag | Description |
|---|---|
| `--size-mib <n>` | Volume size in MiB for `create` (default 1024) |
| `--force`, `-f` | Remove a volume even if it is attached |
| `--state-dir <dir>` | State directory holding the workspace and volume records (default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--output`/`--json`.

## Exit status

`volume` subcommands exit `0` on success; nonzero when the volume cannot be
found, a name collides on create, or `delete` would remove a volume still attached
to a running workspace (without `--force`).

## Related

- [`run`](/cli/run/) - attach with `--volume name:/mount`
- [`create`](/cli/create/) - attach to a persistent workspace
- [Volumes and data](/guides/volumes-and-data/) - the data-flow walkthrough
- [Storage](/concepts/storage/) - how volumes relate to disks and bundles
