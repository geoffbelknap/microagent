---
title: Architecture
description: How the CLI, vmkit core, and backend supervisors fit together.
---

`microagent` is a thin CLI on top of `pkg/vmkit`. `vmkit` builds requests,
dispatches them through a backend supervisor, and writes back structured
responses. Each host OS uses one backend.

```text
your program
  └─ microagent (cmd/microagent)
       └─ vmkit (pkg/vmkit)
            └─ backend supervisor
                 ├─ Firecracker supervisor (Linux, Go JSON exec)
                 └─ Apple VF supervisor (macOS, Swift JSON exec)

OCI image ──► pkg/rootfs ──► ext4 disk ──► VM
```

## Pieces

- **`cmd/microagent`** — the CLI. Parses flags, builds a `vmkit.Request`,
  hands it to the dispatcher, prints the `vmkit.Response`.
- **`pkg/vmkit`** — request/response types and the supervisor interface. The
  shared shape both backends speak.
- **`pkg/rootfs`** — OCI image to ext4 rootfs builder. Pulls layers via
  `oras-go`, writes a sized ext4 image with `mke2fs`.
- **`cmd/microagent-firecracker-supervisor`** — Go executable supervisor for
  Linux Firecracker lifecycle work.
- **`supervisors/applevf`** — Swift executable
  `microagent-applevf-supervisor`. Reads one JSON request from stdin, talks
  to Apple Virtualization.framework, writes one JSON response to stdout.
- **`cmd/microagent-guestinit`** — small guest init that mounts attached
  disks and runs `--setup` / `--exec`.

## Lifecycle of a `microagent run`

1. CLI parses flags into a `vmkit.Request` (kernel path, rootfs path, disks,
   memory, CPUs, identity).
2. If a kernel or rootfs is missing, vmkit triggers
   [`pkg/rootfs`](https://github.com/geoffbelknap/microagent-kit/tree/main/pkg/rootfs)
   or the kernel installer.
3. The dispatcher selects the host backend.
4. The backend supervisor executable handles lifecycle work.
5. Guest init runs `--setup` then `--exec`.
6. State changes are emitted as JSON events. State files live under
   `--state-dir` (default `~/.microagent/...`).
7. On `--keep`, the workspace stays. Otherwise vmkit issues `stop` and
   `delete` to clean up.

## Why Supervisors Are Executable

Running backend supervisors as separate processes lets every language with a
JSON parser drive the same lifecycle protocol and keeps host-specific backend
code out of the main CLI binary. Apple VF also needs this boundary because
Virtualization.framework is Swift-only.

The shared protocol is documented at [Supervisor protocol](/protocol/). The
Apple VF executable protocol is documented at
[Apple VF supervisor](/protocol/applevf/).
