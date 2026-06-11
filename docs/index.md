---
title: microagent
description: Boot real Linux microVMs from OCI images, from the CLI or from Go.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

microagent boots real Linux microVMs from the OCI images you already use, and
gives you a CLI and a Go API to run commands inside them, move files in and
out, and manage their lifecycle. The kernel, disk, and network plumbing is
handled for you.

Each host OS uses one backend: Firecracker on Linux, Apple
Virtualization.framework on macOS, and experimental Hyper-V on Windows. The
choice is automatic. For what microagent deliberately leaves to your control
plane, see [Boundaries](/concepts/boundaries/).

## Choose your path

- **Trying it from the CLI?** The [quickstart](/getting-started/quickstart/)
  boots a microVM and runs a command inside it in minutes. If you already
  think in Docker commands,
  [coming from Docker](/getting-started/coming-from-docker/) maps them to
  their microagent equivalents.
- **Embedding it from Go?** Start with the [library overview](/library/), then
  [run microagent from a Go program](/getting-started/library/first-program/).
- **Connecting an agent or MCP client?** Point it at the MCP stdio endpoint:
  [`microagent serve`](/cli/serve/).

## Sections

- [Getting started](/getting-started/quickstart/): install, quickstart, and the first-agent walkthrough.
- [Library](/library/): Go package overview, reference, and CLI-to-library mapping.
- [Concepts](/concepts/architecture/): architecture, backends, boundaries,
  networking, storage, state and identity, [glossary](/concepts/glossary/).
- [CLI reference](/cli/): every subcommand.
- [Protocol](/protocol/): shared supervisor protocol and backend notes.
- [Guides](/guides/): task-shaped walkthroughs, from one-shot runs to services and snapshots.
- [Security](/security/): trust boundary and reporting.
- [Troubleshooting](/troubleshooting/): common failure modes, indexed by symptom.
