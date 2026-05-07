---
title: microagent-kit
description: Run AI agent workspaces in microVMs.
---

`microagent-kit` provides the `microagent` CLI for running Linux workspaces
inside microVMs. Each host OS uses one backend: Firecracker on Linux, Apple
Virtualization.framework on macOS. Microagent owns the kernel, the OCI-to-disk
conversion, and the VM lifecycle. Identity, policy, credentials, and
control-plane decisions stay outside this project.

## Where to start

- New here? [Install](getting-started/install.md), then
  [run your first VM](getting-started/first-vm.md).
- Need the CLI surface? Jump to the [CLI reference](cli/index.md).
- Integrating? Read the [architecture overview](concepts/architecture.md),
  [networking](concepts/networking.md), the [supervisor protocol](protocol/index.md),
  the [runtime parity contract](protocol/runtime-contract.md), and the
  [Go library](library/go.md).

## Sections

- [Getting started](getting-started/install.md) — install, first VM, named
  workspaces.
- [Concepts](concepts/architecture.md) — architecture, backends, boundaries,
  networking, state and identity.
- [CLI reference](cli/index.md) — every subcommand.
- [Protocol](protocol/index.md) — shared supervisor protocol and backend notes.
- [Library](library/go.md) — exported Go package surface.
- [Security](security.md) — trust boundary and reporting.
