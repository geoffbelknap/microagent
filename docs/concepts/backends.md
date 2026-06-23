---
title: Backends
description: See what each host OS supports before you pick where to run microagent.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-23_

microagent's supported host targets are Linux and macOS. Linux uses
Firecracker, and macOS uses Apple Virtualization.framework. WSL is an intended
Linux compatibility lane when the underlying Linux prerequisites are available.
Windows Hyper-V remains an experimental backend. See
[Platform support](/concepts/platform-support/) for the support policy.

This page is the backend picture - what each backend implements and what it
needs from the host - so you can decide where to deploy and check whether the
host has what it needs. The CLI does not fall back to a cross-host default - if
a request names a backend that does not match the installed host OS, microagent
fails before it builds a rootfs or talks to a supervisor.

| Backend | Host OS | Maturity | Networking modes | Requirements | Notes |
|---|---|---|---|---|---|
| `linux-kvm` | Linux | Supported | `user`, `isolated`, TCP `--publish` | `/dev/kvm`, `/dev/vhost-vsock`, `/dev/net/tun`, `firecracker`, `pasta` for `user` networking | Reference implementation; importable as a Go package. WSL uses this backend when the Linux prerequisites are exposed. |
| `apple-vf` | macOS (Apple silicon) | Supported | `user`, `isolated`, TCP `--publish` | Virtualization.framework, Swift supervisor binary | NAT is macOS-managed |
| `windows-hyperv` | Windows | Experimental | `user` (HNS NAT), `isolated`, TCP `--publish` | Hyper-V / Host Compute Service | Linux guests without WSL or QEMU |

Implemented backends expose the same backend-neutral request and response
structures where they implement a feature. The mechanics under each verb differ
per host. Firecracker and Apple VF share the executable supervisor boundary;
Windows Hyper-V speaks the same `vmkit` protocol inside an experimental Go
supervisor boundary. Networking internals per backend are covered in
[Networking](/concepts/networking/).

## Firecracker (Linux)

- Backend id: `linux-kvm` (the Linux/KVM backend, implemented with Firecracker).
- Uses `microagent-firecracker-supervisor` around the Firecracker process.
  Override the supervisor with `--supervisor` or
  `MICROAGENT_FIRECRACKER_SUPERVISOR`.
- Requires `/dev/kvm`, `/dev/vhost-vsock`, `/dev/net/tun`, working
  unprivileged user namespaces, `pasta` for the default `user` network mode,
  and the `firecracker` binary on `PATH` (or under
  `<prefix>/libexec/firecracker`, or `MICROAGENT_FIRECRACKER`).
- `delete` refuses to remove state while the recorded VM process is still
  running. Use `stop` or `kill` first.
- Supports interactive [`connect`](/cli/connect/) and `connect --send`. Use
  [`logs`](/cli/logs/) when you only need captured serial output.
- The default kernel path is `~/.microagent/kernels/linux-kvm/<arch>/Image`.

### WSL compatibility

WSL uses the `linux-kvm` backend; it is not a separate backend id and
microagent does not fall back from WSL to `windows-hyperv`. It works when the
WSL environment exposes the Linux host capabilities Firecracker needs. Run
`microagent doctor` inside WSL before creating workspaces; it must report the
Linux KVM, vsock, TUN, user namespace, supervisor, guest-init, kernel, and
`pasta` prerequisites as available before a workspace can boot.

Use the Linux backend docs and troubleshooting guidance for WSL unless a page
calls out a WSL-specific behavior.

## Apple VF (macOS)

- Uses Apple Virtualization.framework via the Swift executable supervisor,
  packaged as `microagent-applevf-supervisor`. The supervisor runs one
  invocation per request - there is no resident daemon process on macOS.
  Override with `--supervisor` or `MICROAGENT_APPLEVF_SUPERVISOR`.
- Supports interactive `connect` and `connect --send`.
- Supports `user`, `isolated`, and TCP `--publish` (`user` uses Apple's native
  NAT attachment).
- The default arm64 kernel lives at
  `~/.microagent/kernels/apple-vf/arm64/Image`.

## Windows Hyper-V (experimental)

- Targets Linux microVM-style workspaces on Windows without WSL or QEMU.
- This backend is experimental; behavior and coverage may change.
- Uses the backend name `windows-hyperv` and Host Compute Service through
  `vmcompute.dll`; lifecycle state records HCS compute IDs.
- Consumes VHD root disks at `~/.microagent/workspaces/<name>/rootfs.vhd`, and
  wraps named volumes as VHD-backed ext4 disks.
- Supports the full lifecycle: `host`, `check`, `prepare`, `run`, `start`,
  `inspect`, `connect`, `halt`, `quarantine`, `stop`, `kill`, and `delete`.
  `clone`, `cp`, artifacts, and `commit` ride guest-mediated maintenance boots.
- Supports `user` (HNS NAT) and `isolated` networking plus published TCP
  ports through Hyper-V socket bridging. Live `apply` of host-bind forward
  changes is supported.
- Supports structured [`exec`](/cli/exec/) (buffered and `--stream`) through a
  host TCP listener bridged to the guest exec service over Hyper-V sockets,
  [`connect`](/cli/connect/) over Hyper-V sockets, secrets (materialized,
  on-demand, and audited), model serving, supervised restart, survive-reboot
  via a Scheduled Task, and `perf` footprint/steady sampling from HCS memory
  statistics.
- Fails closed for the direct supervisor `console` command; use
  [`connect`](/cli/connect/), which is the interactive contract on every
  backend.
- See [Windows Hyper-V supervisor](/protocol/windows-hyperv/) for protocol
  details and current limitations (pause/resume is supported; snapshots
  are not supported — HCS-direct VMs have no guest-memory save-state).

## Checking your host

`microagent doctor` reports the active backend, backend-specific host support,
virtualization availability, guest-init availability, and the default kernel
status. Run it before anything else on a new machine.
