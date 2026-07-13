---
title: Library
description: Embed microagent from Go instead of shelling out to the CLI.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-13_

`microagent` is a Go library with a CLI on top. If you are building an agent
runtime, scheduler, local developer tool, or backend service, import the Go
packages instead of spawning the CLI.

## Start here

- [Run microagent from a Go program](/getting-started/library/first-program/)
  boots a VM, runs a command, and tears it down from Go.
- [Go library reference](/library/go/) lists the exported packages, public
  symbols, and CLI-to-library mapping.
- [How microagent fits](/concepts/architecture/) explains when to use the CLI,
  MCP endpoint, or library.

## What the library owns

The packages own lifecycle, rootfs builds, kernel management, image records,
diagnostics, and performance measurement. The CLI is useful for humans and
scripts, but it calls the same packages your Go program can import. The MCP
endpoint follows the same rule: it adapts the package APIs for agent clients.
Orchestration, policy, planning, and LLM behavior stay with the caller; see
[Boundaries](/concepts/boundaries/) for the line microagent does not cross.

## Main packages

The core packages most programs start with:

| Package | Use it for |
|---|---|
| `pkg/workspace` | Create, run, start, inspect, stop, delete, clone, copy files, read logs, collect results, and supervise workspaces. |
| `pkg/rootfs` | Converting OCI images and tar bundles into ext4 rootfs disks. |
| `pkg/kernel` | Installing, verifying, and update-checking default backend kernels. |
| `pkg/imagecache` | Pulling, tagging, listing, removing, and pruning reusable local rootfs baselines. |
| `pkg/diagnostics` | Check whether the current host can boot a VM. |
| `pkg/perf` | Measuring boot, footprint, and steady-state VM performance. |
| `pkg/vmkit` | Shared request/response types and supervisor clients. |

Supporting packages back specific workflows - structured exec types
(`pkg/workspace/exec/protocol`), image commit, models and model runners,
volumes, secrets, and registry auth. The
[Go library reference](/library/go/#exported-packages) lists them all with
their entry points.

## CLI or library?

Use the CLI when you want an operator-facing command, a shell script, or a
quick one-shot run. Use the Go library when microVM lifecycle is part of your
program's control flow and you want typed options, typed responses, and direct
error handling.
