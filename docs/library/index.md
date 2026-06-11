---
title: Library
description: Embed microagent from Go instead of shelling out to the CLI.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

`microagent` is a Go library with a CLI adapter. If you are building an
agent runtime, scheduler, local developer tool, or backend service, use the Go
packages directly instead of spawning `microagent`.

## Start here

- [Run microagent from a Go program](/getting-started/library/first-program/)
  boots a VM, runs a command, and tears it down from Go.
- [Go library reference](/library/go/) lists the exported packages, public
  symbols, and CLI-to-library mapping.
- [Architecture](/concepts/architecture/) explains how the library, CLI, and
  backend supervisors fit together.

## What the library owns

Everything the CLI can do to a microVM, the packages do first: lifecycle,
rootfs builds, kernel management, image records, diagnostics, and performance
measurement all live in library code. The CLI is useful for humans and
scripts, but it is not a separate product surface - it calls the same packages
your Go program can import. The MCP endpoint follows the same rule: it adapts
the existing package APIs for agent clients and does not add orchestration,
policy, planning, or LLM behavior.

## Main packages

| Package | Use it for |
|---|---|
| `pkg/workspace` | Creating, running, starting, inspecting, stopping, deleting, cloning, copying files, reading logs, collecting results, and supervising workspaces. |
| `pkg/rootfs` | Converting OCI images and tar bundles into ext4 rootfs disks. |
| `pkg/kernel` | Installing and verifying default backend kernels. |
| `pkg/imagecache` | Pulling, tagging, listing, removing, and pruning reusable local rootfs baselines. |
| `pkg/diagnostics` | Checking host backend support before trying to boot a VM. |
| `pkg/perf` | Measuring boot, footprint, and steady-state VM performance. |
| `pkg/vmkit` | Backend-neutral supervisor request/response types and executable supervisor clients. |

## CLI or library?

Use the CLI when you want an operator-facing command, a shell script, or a
quick one-shot run. Use the Go library when microVM lifecycle is part of your
program's control flow and you want typed options, typed responses, and direct
error handling.
