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

- New here? [Install](/getting-started/install/), then
  [run your first agent workspace](/getting-started/first-agent/).
- Building an orchestrator? Start with the [Go library](/library/go/).
- Need the command-line tool? Jump to the [CLI reference](/cli/).
- Integrating? Read the [architecture overview](/concepts/architecture/),
  [networking](/concepts/networking/), the [supervisor protocol](/protocol/),
  the [runtime contract](/protocol/runtime-contract/), and the
  [Go library](/library/go/).

## Sections

- [Getting started](/getting-started/install/) — install, first agent workspace, named
  workspaces.
- [Concepts](/concepts/architecture/) — architecture, backends, boundaries,
  networking, state and identity, [glossary](/concepts/glossary/).
- [CLI reference](/cli/) — every subcommand.
- [Protocol](/protocol/) — shared supervisor protocol and backend notes.
- [Library](/library/go/) — exported Go package surface.
- [Security](/security/) — trust boundary and reporting.
- [Troubleshooting](/troubleshooting/) — common failure modes, indexed by symptom.
