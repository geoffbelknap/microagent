---
title: Architecture
description: Understand how the CLI, Go library, and supervisors fit before embedding or extending microagent.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-21_

There are three ways to drive microagent - the CLI, the Go library, and the
MCP server - and all three sit on the same Go packages. The CLI is a thin
adapter with human output and [AX output](/concepts/glossary/) for agent
clients; the MCP stdio endpoint is another adapter over the same packages,
not a second runtime. Linux and macOS are the supported host targets; see
[Platform support](/concepts/platform-support/) for how WSL and experimental
Windows Hyper-V fit. This page maps which piece owns what, so you can pick an
entry point and know where to dig in.

```text
your orchestrator (shell, MCP client, or Go program)
  └─ microagent Go packages
       ├─ pkg/workspace ─ workspace lifecycle and exec
       ├─ pkg/rootfs · pkg/kernel · pkg/imagecache · pkg/diagnostics
       └─ pkg/vmkit ─ supervisor dispatch
            └─ backend supervisor
                 ├─ Firecracker supervisor (Linux, Go JSON exec)
                 ├─ Apple VF supervisor (macOS, Swift JSON exec)
                 └─ Windows Hyper-V supervisor (experimental, Go JSON exec)

OCI image ──► pkg/rootfs ──► ext4 disk ──► microVM
```

## Pieces

Each package exists to answer one question.

`cmd/microagent` is the CLI: it parses flags, calls the packages, and renders
human, JSON, or AX output. `microagent serve mcp` adds the stdio MCP adapter
so MCP clients drive the same package surface as tools - see
[Serve microagent over MCP](/guides/mcp-server/).

`pkg/workspace` owns what a workspace *is* - its manifest, disks, identity,
and results - and everything you do to a running one. The supporting packages
stay deliberately small. `pkg/rootfs` turns an OCI image into a bootable ext4
disk. `pkg/kernel` manages default kernels. `pkg/imagecache` keeps reusable
rootfs baselines and backs the `images` CLI surface. `pkg/diagnostics` powers
`microagent doctor`. `pkg/vmkit` defines the request/response shape every
supervisor speaks.

Linux callers can import `pkg/supervisors/firecracker` directly when they do
not want a subprocess. The executable supervisors still matter:
`cmd/microagent-firecracker-supervisor` wraps the Go Firecracker implementation
as JSON-in / JSON-out, and `supervisors/applevf` ships the Swift
`microagent-applevf-supervisor` for Apple's Virtualization.framework.
`cmd/microagent-guestinit` is the small guest init that mounts attached disks
and runs `--setup` / `--exec`.

## Lifecycle of a `microagent run`

1. The CLI parses flags into `pkg/workspace` options.
2. `pkg/workspace` prepares disks, builds the rootfs with `pkg/rootfs`, records
   verification, writes the manifest, and builds a `vmkit.Request`.
3. The dispatcher selects the host backend.
4. The backend supervisor executable handles VM lifecycle work.
5. Guest init runs `--setup` then `--exec`.
6. State changes are emitted as JSON events. State files live under
   `--state-dir` (default `~/.microagent/...`).
7. On `--keep`, the workspace stays. Otherwise the workspace API cleans up
   local state.

[Run one-shot commands](/guides/one-shot-runs/) walks this flow from the
operator's seat. Go callers can use the same package flow directly without
invoking the CLI: see the [library overview](/library/) for the recommended
entry points and the [Go library reference](/library/go/) for the exported
package surface.

## Why supervisors are separate executables

Each backend's supervisor ships as its own JSON-in / JSON-out binary. That
keeps host-specific backend code out of the main CLI and lets anything that can
spawn a subprocess and parse JSON drive the same protocol: Go, Python, Rust,
Node, or shell scripts. Apple VF also needs this split because
Virtualization.framework is Swift-only.

The shared protocol is documented at [supervisor protocol](/protocol/). The
Apple VF executable protocol is documented at
[Apple VF supervisor](/protocol/applevf/).
