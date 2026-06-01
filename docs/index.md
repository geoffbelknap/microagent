---
title: microagent
description: Run AI agent workspaces in microVMs.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

`microagent` is a Go library and CLI for running Linux workspaces inside
microVMs. The Go packages are the integration surface; the CLI is the operator
and scripting layer over those packages. Each host OS uses one backend:
Firecracker on Linux, Apple Virtualization.framework on macOS, and experimental
Windows Hyper-V on Windows.

microagent owns the substrate work: kernel management, OCI-to-disk conversion,
local image records, VM lifecycle, networking/vsock wiring, console access,
stopped-disk file transfer, structured exec, structured results, artifacts,
readiness, runtime verification, and the MCP stdio adapter over those APIs.
Identity, policy, credentials, planning, and control-plane decisions stay
outside this project.

For one-shot use, `microagent run IMAGE [COMMAND ARG...]` boots a VM, runs the
command, and removes scratch state. For persistent workspaces, use `create`,
`start`, `connect`, `halt`, `stop`, and `delete`. Container-style aliases such
as `-e`, `-p`, `-v`, `--name`, and `--rm` are available only where they map
cleanly to microVM behavior.

## Where to start

If you are trying it from the CLI, start with [Install](/getting-started/install/),
[run your first microVM](/getting-started/cli/first-microvm/), then
[run your first agent](/getting-started/cli/first-agent/). If you are embedding
it from Go, start with the [library overview](/library/) and the
[first program](/getting-started/library/first-program/). The **Sections**
index below is the reference map for everything else - concepts, CLI, protocol,
and recipes.

## Sections

- [Getting started](/getting-started/install/): install, plus CLI and library quickstarts.
- [Library](/library/): Go package overview, reference, and CLI-to-library mapping.
- [Concepts](/concepts/architecture/): architecture, backends, boundaries,
  networking, state and identity, [glossary](/concepts/glossary/).
- [CLI reference](/cli/): every subcommand.
- [Protocol](/protocol/): shared supervisor protocol and backend notes.
- [Recipes](/recipes/): end-to-end examples.
- [Security](/security/): trust boundary and reporting.
- [Troubleshooting](/troubleshooting/): common failure modes, indexed by symptom.
