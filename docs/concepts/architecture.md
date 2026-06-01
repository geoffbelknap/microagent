---
title: Architecture
description: How the Go library, CLI, and backend supervisors fit together.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

`microagent` is a Go library with a CLI adapter. The library packages handle
workspace lifecycle, rootfs builds, kernel management, image cache management,
diagnostics, shared request/response types, structured exec, readiness, and
backend supervisor dispatch. The CLI has human output for operators and AX
output for agent clients. The MCP stdio endpoint is an adapter over the same
package surface, not a second runtime. Each host OS uses one backend.

```text
your orchestrator
  └─ microagent Go packages
       ├─ workspace lifecycle, readiness, exec, artifacts, logs, network, supervision
       ├─ rootfs, kernel, image cache, diagnostics
       └─ vmkit supervisor dispatch
            └─ backend supervisor
                 ├─ Firecracker supervisor (Linux, Go JSON exec)
                 └─ Apple VF supervisor (macOS, Swift JSON exec)

OCI image ──► pkg/rootfs ──► ext4 disk ──► VM
```

## Pieces

`cmd/microagent` parses flags, calls the Go packages, and renders human UX
output, JSON output, or AX output for agent clients. `microagent serve mcp`
adds the stdio MCP adapter for workspace lifecycle, inspect/status, structured
exec, images, copy/artifacts, cost estimation, and capability discovery.

`pkg/workspace` handles lifecycle, manifests, state, results, readiness,
structured exec, artifacts, logs, network, file copy, clone, and optional
supervision. The supporting packages are deliberately small: `pkg/kernel`
manages default kernels, `pkg/imagecache` manages reusable rootfs baselines,
`pkg/diagnostics` checks host support, `pkg/vmkit` defines the shared
request/response shape, and `pkg/rootfs` turns OCI images into ext4 disks.

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

Go callers can use the same package flow directly without invoking the CLI.
See the [library overview](/library/) for the recommended entry points and
the [Go library reference](/library/go/) for the exported package surface.

## Why supervisors are separate executables

Each backend's supervisor ships as its own JSON-in / JSON-out binary. That
keeps host-specific backend code out of the main CLI and lets anything that can
spawn a subprocess and parse JSON drive the same protocol: Go, Python, Rust,
Node, or shell scripts. Apple VF also needs this split because
Virtualization.framework is Swift-only.

The shared protocol is documented at [supervisor protocol](/protocol/). The
Apple VF executable protocol is documented at
[Apple VF supervisor](/protocol/applevf/).
