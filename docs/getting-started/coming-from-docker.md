---
title: Coming from Docker
description: Map the Docker commands you already know to their microagent equivalents.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-14_

If you think in Docker commands, most of your muscle memory carries over. The
big difference: each "container" is a real microVM with its own kernel, and
the named persistent unit is called a workspace. This page maps the commands
first, then covers what is intentionally different.

## Command map

| Docker | microagent | Notes |
|---|---|---|
| `docker run --rm IMAGE CMD` | `microagent run IMAGE CMD` | One-shot: boots a microVM, runs the command, removes scratch state. Disposable is the default; pass `--keep` to keep state. |
| `docker run -d --name web IMAGE` | `microagent create --name web --image IMAGE`, then `microagent start web` | A workspace persists between starts. Use `--service-command` for a long-running process. |
| `docker ps` | `microagent list` | Lists workspaces. |
| `docker exec web CMD` | `microagent exec web -- CMD` | Structured result with exit code, stdout, and stderr. Note the `--` before the command. |
| `docker exec -it web bash` | `microagent connect web` | Opens the workspace console. |
| `docker stop web` | `microagent stop web` | Graceful shutdown signal. If the VM doesn't shut down cleanly, follow up with `microagent kill`. See [halt is not stop](#halt-is-not-stop). |
| `docker rm web` | `microagent delete web` | Prompts before deleting; if the workspace is running it offers to stop it first. `--force` kills and deletes. |
| `docker cp app.py web:/srv/` | `microagent cp app.py web:/srv/` | Same shape, but works on a stopped workspace: files move through the disk image, not a running daemon. |
| `docker logs web` | `microagent logs web` | Captured serial console output. `-f` follows. |
| `docker images` | `microagent image list` | Lists local image records. |
| `docker commit web REF` | `microagent commit web REF` | Snapshots a stopped workspace's rootfs into an OCI image. |
| `docker network create/ls/rm` | `microagent network create/list/delete` | Workspaces join a named network with `--network-name`. |
| `docker volume create/ls/inspect/rm` | `microagent volume create/list/status/delete` | Named volumes are managed ext4 disks. Attach with `-v data:/work`. |

The familiar flags work where they map cleanly to microVM behavior: `-e`
(guest environment variable), `-p` (publish a TCP port), `--name`, `--rm`,
and a deliberately narrow `-v` that accepts named volumes, tar bundles, and
ext4 disk images, not host directories. Unsupported features such as
`--privileged`, namespace flags, and host bind mounts fail with targeted guidance
instead of being silently translated.

## What's intentionally different

### No bind mounts

There is no `-v /host/dir:/guest/dir`. A volume is a managed ext4 disk (or a
tar bundle built into one), and everything the guest reads or writes is a
block device. That is the point of the microVM boundary: the guest never
shares a live host filesystem. To move a directory in, package it as a tar
bundle; to move files out, use `microagent cp` or declare `--output`
artifacts. See [Storage](/concepts/storage/).

### No --privileged

The guest already has its own kernel and full root inside the microVM, so
there is no privileged mode to escalate to. Host access stays on the host.

### No build command

microagent does not build images. Build with the tooling you use today
(Docker, Buildah, your CI) and point `microagent run` at the result; it
consumes standard OCI images. To capture changes a workspace made to its
rootfs, [`microagent commit`](/cli/commit/) snapshots a stopped workspace
back into an OCI image.

### Halt is not stop

Docker has `stop` and `kill`. microagent adds a third verb, and the
distinction matters for what you can do next:

- **halt** - clean disk-preserving shutdown. The VM exits, the disk stays,
  and `start` boots the same disk back up.
- **stop** - graceful shutdown signal (SIGTERM on Firecracker, the equivalent
  on Apple VF). If the VM doesn't shut down cleanly, follow up with
  `microagent kill`.
- **kill** - hard terminate, for when `stop` doesn't return.

All three leave the disk in place. `halt` is the deliberate "park it and
start it later" verb; `stop` and `kill` are for making a VM process go away.

`delete` prompts before deleting; if the workspace is running it offers to
stop it first. `--force` kills and deletes. The
[glossary](/concepts/glossary/#lifecycle-vocabulary) has the full lifecycle
vocabulary, including `quarantine`.

## What's next

- [Quickstart](/getting-started/quickstart/) - boot your first microVM.
- [Persistent workspaces](/guides/persistent-workspaces/) - the create, start, halt, delete loop in practice.
- [`microagent run`](/cli/run/) - every flag on the one-shot path.
