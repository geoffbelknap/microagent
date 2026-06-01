---
title: Glossary
description: Terms used throughout the microagent docs and what they actually mean.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

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
- **workspace** - a named, persistent microVM. Disk, identity, and event history all stick around between starts. The thing you create, halt, and restart.

## Control surface

- **supervisor** - a small JSON-in / JSON-out executable that owns lifecycle for one backend (`microagent-firecracker-supervisor`, `microagent-applevf-supervisor`, plus an experimental Windows Hyper-V supervisor). Anything that can spawn a subprocess and parse JSON can drive it.
- **mediation channel** - a guest-to-host vsock contract for the agent's calls into your host control plane. Declared, required by default, and fail-closed unless you explicitly opt out.
- **state directory** - where workspace records live on the host (default `~/.microagent/`).
- **AX mode** - the agent-experience output mode (`--mode=ax`). stdout is structured JSON for agent clients; UX mode is the human-readable default. The MCP endpoint always uses AX output.
- **readiness** - structured signals on a status response (`guestReady`, `shellReady`, `execReady`, `resultReady`, `mediationReady`) so callers can sequence work without polling files or serial logs.

## Lifecycle vocabulary

These five words are not synonyms.

- **halt** - clean disk-preserving shutdown. The VM exits, the disk stays. `start` boots the same disk back up.
- **stop** - graceful shutdown signal (SIGTERM on Firecracker, equivalent on Apple VF). Falls back to `kill` if it doesn't return.
- **kill** - hard terminate (SIGKILL or equivalent). For when `stop` doesn't return.
- **quarantine** - sever host-side network and mediation while preserving disk and event history. The VM may still be running. A forensic state, not a normal stopped state - you must halt, stop, or kill it before you can `start` it again.
- **delete** - remove the workspace and its state. Refuses while a VM process is still running; halt or stop first.
