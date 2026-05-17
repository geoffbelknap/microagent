---
title: microagent
description: Run AI agent workspaces in microVMs.
---

`microagent` provides Go packages and the `microagent` CLI for running
Linux workspaces inside microVMs. Each host OS uses one backend: Firecracker on
Linux, Apple Virtualization.framework on macOS, and experimental Windows
Hyper-V on Windows. microagent owns the kernel, OCI-to-disk conversion, local
image records, VM lifecycle, networking/vsock wiring, console access, file
transfer for stopped disks, structured results, artifacts, and runtime
verification. Identity, policy, credentials, and control-plane decisions stay
outside this project.

For one-shot use, `microagent run IMAGE [COMMAND ARG...]` boots a VM, runs the
command, and removes scratch state. For persistent workspaces, use `create`,
`start`, `connect`, `halt`, `stop`, and `delete`. Container-style aliases such
as `-e`, `-p`, `-v`, `--name`, and `--rm` are available only where they map
cleanly to microVM behavior.

## Where to start

Pick the path that matches what you're doing:

- **Trying it out from the CLI** — [Install](/getting-started/install/),
  [run your first microVM](/getting-started/cli/first-microvm/),
  [run your first agent](/getting-started/cli/first-agent/), then
  [keep workspaces around](/getting-started/cli/named-workspaces/).
- **Building with the library (agent runtime, microVM orchestrator, or just
  microVMs from Go)** — [Install](/getting-started/install/), then
  [run microagent from a Go program](/getting-started/library/first-program/).
  Reference: [Go library](/library/go/).
- **Integrating with the protocol** — [architecture overview](/concepts/architecture/),
  [networking](/concepts/networking/), [supervisor protocol](/protocol/), and
  the [runtime contract](/protocol/runtime-contract/).

## Sections

- [Getting started](/getting-started/install/) — install, plus a CLI quickstart
  and a library quickstart.
- [Concepts](/concepts/architecture/) — architecture, backends, boundaries,
  networking, state and identity, [glossary](/concepts/glossary/).
- [CLI reference](/cli/) — every subcommand.
- [Protocol](/protocol/) — shared supervisor protocol and backend notes.
- [Library](/library/go/) — exported Go package surface.
- [Recipes](/recipes/) — end-to-end examples.
- [Security](/security/) — trust boundary and reporting.
- [Troubleshooting](/troubleshooting/) — common failure modes, indexed by symptom.
