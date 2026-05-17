---
title: Backends
description: One backend per host OS. Same lifecycle surface, different mechanics.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-17_

microagent installs with one backend for the host OS: Firecracker on Linux,
Apple VF on macOS, and experimental Windows Hyper-V on Windows. The CLI does
not fall back to a cross-host default. If a request names a backend that does
not match the installed host OS, microagent fails before it builds a rootfs or
talks to a supervisor.

| Backend | Host OS | Supervisor | `connect` | Process model |
|---|---|---|---|---|
| `firecracker` | Linux | Go executable supervisor (`microagent-firecracker-supervisor`) | supported | Supervisor records VM PID; `quarantine` preserves it, `stop` sends SIGTERM, `kill` sends SIGKILL |
| `apple-vf` | macOS | Swift executable supervisor (`microagent-applevf-supervisor`) | supported | One supervisor invocation per request |
| `windows-hyperv` | Windows | Experimental Go supervisor boundary for Linux guests through HCS / Hyper-V | supported through Hyper-V sockets | HCS compute systems are created through `vmcompute.dll`; lifecycle state records HCS compute IDs |

Backends expose the same backend-neutral request and response structures, but
the host mechanics differ. Firecracker and Apple VF share the same executable
supervisor-shaped request/response boundary. Windows Hyper-V uses the same
`vmkit` protocol inside the Go supervisor boundary.

## Firecracker (Linux)

- Uses `microagent-firecracker-supervisor` around the Firecracker process.
- Override the supervisor with `--supervisor` or
  `MICROAGENT_FIRECRACKER_SUPERVISOR`.
- Requires `/dev/kvm` and the `firecracker` binary on `PATH` (or under
  `<prefix>/libexec/firecracker`, or `MICROAGENT_FIRECRACKER`).
- `delete` refuses to remove state while the recorded VM process is still
  running. Use `stop` or `kill` first.
- Supports interactive `connect` and `connect --send`. Use
  [`logs`](/cli/logs/) when you only need captured serial output.
- The default kernel path is `~/.microagent/kernels/firecracker/<arch>/Image`.

## Apple VF (macOS)

- Uses Apple Virtualization.framework via the Swift executable supervisor.
- Supports interactive `connect` and `connect --send`.
- Supports `nat`, `isolated`, and TCP `--publish`. Native bridged networking
  is implemented, but public builds fail closed because Apple gates it behind
  the restricted `com.apple.vm.networking` entitlement.
- The supervisor is packaged as `microagent-applevf-supervisor`. Override with
  `--supervisor` or `MICROAGENT_APPLEVF_SUPERVISOR`.
- The default arm64 kernel lives at
  `~/.microagent/kernels/apple-vf/arm64/Image`.

## Windows Hyper-V (experimental)

- Targets Linux microVM-style workspaces on Windows without WSL or QEMU.
- Uses the backend name `windows-hyperv`.
- Uses Host Compute Service through `vmcompute.dll`.
- Consumes VHD root disks at
  `~/.microagent/workspaces/<name>/rootfs.vhd`.
- Supports `host`, `check`, `prepare`, `run`, `start`, `inspect`, `connect`,
  `halt`, `quarantine`, `stop`, `kill`, and `delete` experimentally.
- Supports HNS NAT networking and published TCP ports through Hyper-V socket
  bridging.
- Fails closed for the direct supervisor `console` command; use
  [`connect`](/cli/connect/).
- See [Windows Hyper-V supervisor](/protocol/windows-hyperv/) for protocol
  details and current limitations.

## Selecting a host

`microagent doctor` reports the active backend, backend-specific host support,
virtualization availability, guest-init availability, and the default kernel
status.
