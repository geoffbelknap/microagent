---
title: Glossary
description: Terms used throughout the microagent docs and what they actually mean.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

A handful of terms come up often enough that it's worth pinning them down before you read the rest of the docs. The lifecycle words in particular are easy to confuse - and the distinctions matter for what you can do next.

## Project

- **microagent** - the project: Go library, CLI, and backend supervisors.
- **`microagent`** - the CLI binary. A thin shell over the Go library.
- **library** - the Go packages (`pkg/workspace`, `pkg/rootfs`, and friends) that do the actual work. Importable from your own program when you'd rather not shell out.

## VMs and what's inside them

- **backend** - how the host OS runs VMs. Linux uses Firecracker. macOS uses Apple Virtualization.framework. Windows uses Hyper-V (experimental). One backend per host; the choice is automatic.
- **substrate** - the host-side VM substrate microagent owns: kernel management, OCI-to-disk conversion, VM lifecycle, and host/guest wiring. Everything below the workspace and above the hypervisor.
- **microVM** - the small, fast VM each workspace runs in. Booted by the backend.
- **guest** - the Linux userspace inside the microVM. What your OCI image becomes once it's booted.
- **rootfs** - the ext4 disk image the guest boots from. Built from an OCI image.
- **kernel** - the Linux kernel image the microVM boots. Backend-specific; the default is downloaded on first use.
- **workspace** - a named, persistent microVM. Disk, identity, and event history all stick around between starts. The thing you create, halt, and restart. See [Keep a persistent workspace](/guides/persistent-workspaces/).
- **agent** - the program you run inside a workspace. microagent doesn't define it or impose a framework; in these docs it means a small LLM loop with tools (see [run your first agent](/getting-started/cli/first-agent/)).
- **snapshot** - a point-in-time checkpoint of a running workspace's memory and disk. Restore it in place, or fork independent copies from it. See [Snapshot and fork workspaces](/guides/snapshots-and-forking/).

## Storage and networking

- **disk** - an ext4 image attached to a workspace at a mountpoint, in addition to the rootfs. microagent never exposes host directories; everything the guest reads or writes is a block device. See [Storage](/concepts/storage/).
- **bundle** - a tar archive (`.tar`/`.tar.gz`/`.tgz`) built into a one-shot ext4 disk at start. The portable way to get a directory's contents into a workspace. See [Use volumes and move data](/guides/volumes-and-data/).
- **named volume** - a platform-managed ext4 disk addressed by name, with a lifecycle independent of any one workspace. Single-attach (one running workspace at a time); the in-boundary analog of a container volume. Attach with `-v name:/mount`. See [Use volumes and move data](/guides/volumes-and-data/).
- **named network** - a user-defined network workspaces join with `--network-name`. Members get a stable IP from its subnet, share a managed bridge so they reach each other, and resolve each other via `/etc/hosts`. Workspace attachment is currently implemented by Firecracker on Linux. See [Connect workspaces on a named network](/guides/networking/) and the [internals](/concepts/networking/#named-networks).

## Control surface

- **supervisor** - a small JSON-in / JSON-out executable that owns lifecycle for one backend (`microagent-firecracker-supervisor`, `microagent-applevf-supervisor`, plus an experimental Windows Hyper-V supervisor). Anything that can spawn a subprocess and parse JSON can drive it.
- **mediation channel** - a guest-to-host vsock contract for the agent's calls into your host control plane. Declared, required by default, and fail-closed unless you explicitly opt out. See [Build agents on the mediation channel](/guides/agents-and-mediation/).
- **state directory** - where workspace records live on the host (default `~/.microagent/`).
- **AX mode** - the agent-experience output mode (`--mode=ax`). stdout is structured JSON for agent clients; UX mode is the human-readable default. The [MCP endpoint](/guides/mcp-server/) always uses AX output.
- **readiness** - structured signals on a status response (`guestReady`, `shellReady`, `execReady`, `resultReady`, `mediationReady`) so callers can sequence work without polling files or serial logs. See [State and identity](/concepts/state-and-identity/#readiness).

## Lifecycle vocabulary

These six words are not synonyms.

- **halt** - clean disk-preserving shutdown. The VM exits, the disk stays. `start` boots the same disk back up.
- **pause** - memory-state suspend, not a shutdown. Freezes a running workspace's vCPUs while preserving memory and disk; `resume` thaws it back to running exactly where it left off. Firecracker only; `exec`, `connect`, and `stats` are rejected while paused. Unlike `halt`, nothing is discarded and nothing reboots.
- **stop** - graceful shutdown signal (SIGTERM on Firecracker, equivalent on Apple VF). If the VM hasn't exited after five seconds, `stop` marks the workspace `failed` and returns an error; it never escalates on its own - following up with `kill` is your move.
- **kill** - hard terminate (SIGKILL or equivalent). For when `stop` doesn't return.
- **quarantine** - sever host-side network and mediation while preserving disk and event history. The VM may still be running. A forensic state, not a normal stopped state - you must halt, stop, or kill it before you can `start` it again.
- **delete** - remove the workspace and its state. Refuses while a VM process is still running; halt or stop first.
