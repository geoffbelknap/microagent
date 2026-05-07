---
title: microagent-kit
description: Run AI agent workspaces in microVMs.
---

`microagent-kit` provides Go packages and the `microagent` CLI for running
Linux workspaces inside microVMs. Each host OS uses one backend: Firecracker on
Linux, Apple Virtualization.framework on macOS. microagent-kit owns the kernel,
the OCI-to-disk conversion, and the VM lifecycle. Identity, policy, credentials,
and control-plane decisions stay outside this project.

## Where to start

- New here? [Install](getting-started/install.md), then
  [run your first agent workspace](getting-started/first-agent.md).
- Building an orchestrator? Start with the [Go library](library/go.md).
- Need the command-line tool? Jump to the [CLI reference](cli/index.md).
- Integrating? Read the [architecture overview](concepts/architecture.md),
  [networking](concepts/networking.md), the [supervisor protocol](protocol/index.md),
  the [runtime parity contract](protocol/runtime-contract.md), and the
  [Go library](library/go.md).

## Sections

- [Getting started](getting-started/install.md) — install, first agent workspace, named
  workspaces.
- [Concepts](concepts/architecture.md) — architecture, backends, boundaries,
  networking, state and identity, [glossary](concepts/glossary.md).
- [CLI reference](cli/index.md) — every subcommand.
- [Protocol](protocol/index.md) — shared supervisor protocol and backend notes.
- [Library](library/go.md) — exported Go package surface.
- [Security](security.md) — trust boundary and reporting.
- [Stability](stability.md) — what microagent-kit promises, what it doesn't.
- [Troubleshooting](troubleshooting.md) — common failure modes, indexed by symptom.
