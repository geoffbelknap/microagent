---
title: Glossary
description: Terms used throughout the microagent docs and what they mean.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-13_

A handful of terms come up often enough that it's worth pinning them down before you read the rest of the docs. The lifecycle words in particular are easy to confuse - and the distinctions matter for what you can do next.

## Project

- **microagent** - the project: Go library, CLI, and host supervisors.
- **`microagent`** - the CLI binary. A thin shell over the Go library.
- **library** - the Go packages (`pkg/workspace`, `pkg/rootfs`, and the rest) that do the work. Importable from your own program when you'd rather not shell out.

## VMs and what's inside them

- **backend** - the host-specific path microagent uses to run a microVM. Linux and macOS are supported host targets; WSL is a Linux compatibility lane. See [Host requirements](/concepts/backends/).
- **microVM** - the small, fast VM each workspace runs in. Booted by the backend.
- **guest** - the Linux userspace inside the microVM. What your OCI image becomes once it's booted.
- **rootfs** - the ext4 disk image the guest boots from. Built from an OCI image.
- **kernel** - the Linux kernel image the microVM boots. Backend-specific; the default is downloaded on first use.
- **workspace** - a named, persistent microVM. Disk, identity, and event history all stick around between starts. The thing you create, halt, and restart. See [Keep a persistent workspace](/guides/persistent-workspaces/).
- **agent** - the program you run inside a workspace. microagent doesn't define it or impose a framework; in these docs it means a small LLM loop with tools (see [run your first agent](/getting-started/cli/first-agent/)).
- **snapshot** - a point-in-time checkpoint of a running workspace's memory and disk. Restore it in place, or fork independent copies from it. See [Snapshot and fork workspaces](/guides/snapshots-and-forking/).
- **fork** - a new workspace created from an existing workspace's snapshot (`create --from-snapshot`). The fork gets a fresh identity and a private copy of the snapshot's rootfs, then resumes from the snapshot's memory state. The source workspace and snapshot are unchanged. See [`create`](/cli/create/#fork-from-a-snapshot).
- **clone** - a disk-only copy of a stopped workspace into a new workspace (`microagent clone`). No memory state is involved, so the source must be stopped. Use a fork when you want the memory state too. See [`clone`](/cli/clone/).
- **baseline** - a reusable rootfs recorded for an image. `create` and `run` reuse the baseline instead of rebuilding when the image and guest init match. Managed through [`image`](/cli/image/).
- **config disk** - the read-only block device the host regenerates for every boot, carrying the run config and declared files into the guest. Because per-workspace settings travel here, every rootfs built from the same image stays byte-identical.
- **Agentfile** - a workspace spec (`microagent.yaml`) whose `agent:` block declares egress and credential settings, making the spec a build-free recipe for running an agent. See [`spec`](/cli/spec/#agentfile-the-agent-block).

## Storage and networking

- **disk** - an ext4 image attached to a workspace at a mountpoint, in addition to the rootfs. microagent never exposes host directories; everything the guest reads or writes is a block device. See [Storage](/concepts/storage/).
- **bundle** - a tar archive (`.tar`/`.tar.gz`/`.tgz`) built into a one-shot ext4 disk at start. The portable way to get a directory's contents into a workspace. See [Use volumes and move data](/guides/volumes-and-data/).
- **named volume** - a platform-managed ext4 disk addressed by name, with a lifecycle independent of any one workspace. Single-attach (one running workspace at a time); the in-boundary analog of a container volume. Attach with `-v name:/mount`. See [Use volumes and move data](/guides/volumes-and-data/).
- **network mode** - a workspace has one of two modes: `user` (the default) gives the guest unprivileged outbound IPv4 plus any published TCP ports; `isolated` gives it no network device at all. See [Networking](/concepts/networking/).

## Control

- **supervisor** - the host-side helper microagent uses to start, stop, and inspect microVMs on the current platform. Most users never call it directly.
- **mediation channel** - a guest-to-host vsock path for the agent's calls into your host control plane. Declared, required by default, and fail-closed unless you explicitly opt out. **Not the same as egress mediation** (below); they only share the word "mediation". See [Build agents on the mediation channel](/guides/agents-and-mediation/).
- **egress mediation** - the capture-and-control layer over the guest's *ordinary network egress* (the TCP/UDP/DNS it sends out of its network device). On by default (`broker` mode), with `mitm` and `off` as the alternatives. It polices destinations, records every decision for `microagent egress`, and can confine a workspace to an allowlist (`--egress-lock-allowlist`); only `mitm` mode intercepts TLS with a per-workspace CA. Distinct from the vsock mediation channel above. See [Egress mediation](/concepts/egress-mediation/).
- **mediator** - the small host-side process every mediated workspace's network traffic passes through. The mediator, not the guest, writes the egress audit record, so a compromised task can neither forge nor suppress it.
- **broker** - the default egress mediation mode: public internet allowed, "the inside" denied, every decision audited, and allowed TLS spliced without interception. Its broker endpoints can also inject credentials host-side. See [Egress mediation](/concepts/egress-mediation/).
- **state directory** - where workspace records live on the host (default `~/.microagent/`).
- **AX (agent experience)** - the design discipline applied to microagent's [MCP endpoint](/guides/mcp-server/): typed tools, compact decision-relevant results, actionable errors, bounded context, idempotency, confirmations, and clear next actions. AX is not a CLI output mode or a separate transport.
- **readiness** - structured signals on a status response (`guestReady`, `shellReady`, `execReady`, `resultReady`, `mediationReady`) so callers can sequence work without polling files or serial logs. See [State and identity](/concepts/state-and-identity/#readiness).

## Lifecycle vocabulary

These words are not synonyms.

- **halt** - clean disk-preserving shutdown; the canonical verb. The VM exits, the disk stays, and `start` boots the same disk back up. In the CLI, `stop` is an alias of `halt` - it runs the identical mechanism and produces the identical `halted` outcome, so `stop` keeps working if you are used to it. The library's `Control("stop")` command still records `stopped`, not `halted` - same shutdown mechanism, different terminal state; see [Go library reference](/library/go/#workspace-api). Guest PID 1 forwards the OCI `StopSignal` and powers off; the host allows about 15 seconds for the sequence. If it doesn't exit in time, `halt`/`stop` return an error without escalating; follow up with `kill` when you want the hard stop.
- **pause** - memory-state suspend, not a shutdown. Freezes a running workspace's vCPUs while preserving memory and disk; `resume` thaws it back to running exactly where it left off. `exec`, `connect`, and `stats` are rejected while paused. Unlike `halt`, nothing is discarded and nothing reboots.
- **kill** - hard termination. For when `halt`/`stop` doesn't return.
- **quarantine** - atomically mark containment, freeze guest execution, sever network, brokers, published ports, and other host authority, capture evidence while frozen, then stop into durable custody. The marker blocks ordinary start, resume, restore, and deletion even after a crash.
- **delete** - remove the workspace and its state. If the VM is still running, `delete` asks before stopping it (`--yes` or `--force` skips the prompt and stops it for you).
