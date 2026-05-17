---
title: Architecture
description: How the Go library, CLI, and backend supervisors fit together.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-17_

`microagent` is a Go library with a CLI adapter. The library packages own
workspace lifecycle, rootfs builds, kernel management, image cache management,
diagnostics, shared request/response types, and backend supervisor dispatch.
Each host OS uses one backend.

```text
your orchestrator
  └─ microagent Go packages
       ├─ workspace lifecycle, artifacts, logs, network, supervision
       ├─ rootfs, kernel, image cache, diagnostics
       └─ vmkit supervisor dispatch
            └─ backend supervisor
                 ├─ Firecracker supervisor (Linux, Go JSON exec)
                 └─ Apple VF supervisor (macOS, Swift JSON exec)

OCI image ──► pkg/rootfs ──► ext4 disk ──► VM
```

## Pieces

- **`cmd/microagent`** — the CLI adapter. Parses flags, calls the Go packages,
  and renders structured or human-readable output.
- **`pkg/workspace`** — workspace lifecycle, manifests, state, results,
  artifacts, logs, network, file copy, clone, and optional supervision loop.
- **`pkg/kernel`** — default kernel manifest plus install, verify, and support
  checks.
- **`pkg/imagecache`** — reusable rootfs baseline cache and image index.
- **`pkg/diagnostics`** — host/backend preflight checks and support summaries.
- **`pkg/vmkit`** — request/response types and the supervisor interface. The
  shared shape both backends speak.
- **`pkg/rootfs`** — OCI image to ext4 rootfs builder. Pulls layers via
  `oras-go`, writes a sized ext4 image with `mke2fs`.
- **`pkg/supervisors/firecracker`** — Go implementation of the Firecracker
  supervisor, importable directly on Linux when you'd rather not spawn a
  subprocess.
- **`cmd/microagent-firecracker-supervisor`** — Go executable supervisor for
  Linux Firecracker lifecycle work. Wraps `pkg/supervisors/firecracker` as a
  JSON-in / JSON-out binary.
- **`supervisors/applevf`** — Swift executable
  `microagent-applevf-supervisor`. Reads one JSON request from stdin, talks
  to Apple Virtualization.framework, writes one JSON response to stdout.
- **`cmd/microagent-guestinit`** — small guest init that mounts attached
  disks and runs `--setup` / `--exec`.

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

Each backend's supervisor ships as its own JSON-in / JSON-out binary. That's deliberate: it keeps host-specific backend code out of the main CLI and lets anything that can spawn a subprocess and parse JSON drive the same protocol — Go, Python, Rust, Node, shell scripts. Apple VF also requires this boundary, because Virtualization.framework is Swift-only.

The shared protocol is documented at [supervisor protocol](/protocol/). The
Apple VF executable protocol is documented at
[Apple VF supervisor](/protocol/applevf/).
