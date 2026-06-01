---
title: Backends
description: One backend per host OS. Same lifecycle surface, different mechanics.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

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
- Supports interactive [`connect`](/cli/connect/) and `connect --send`. Use
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

## Backend validation (for contributors)

This is contributor procedure for validating backends end-to-end, not
conceptual background. Skip it unless you are running the live test suites.

### Apple VF validation runbook

Run Apple VF validation on Apple silicon macOS, not inside a Linux or KVM
environment. The host needs Go, the Xcode command line tools with Swift,
Virtualization.framework support, network access for OCI image pulls, and
`e2fsprogs` tools (`mke2fs` and `debugfs`). Homebrew installs those tools under
`/opt/homebrew/opt/e2fsprogs/sbin`, which the smoke scripts check
automatically.

Build and ad-hoc sign the supervisor before live runs:

```bash
scripts/dev/applevf-supervisor-build.sh
```

Install or verify the default Apple VF kernel if the host does not already have
one at `~/.microagent/kernels/apple-vf/arm64/Image`:

```bash
go run ./cmd/microagent --json kernel install --backend apple-vf
go run ./cmd/microagent --json doctor --backend apple-vf
```

Use the unified E2E runner for repeatable macOS validation:

```bash
scripts/dev/microagent-e2e.sh --list
scripts/dev/microagent-e2e.sh contract help-usage registry-auth text-output
scripts/dev/microagent-e2e.sh \
  public-surface \
  lifecycle \
  networking \
  transport \
  supervision
```

On Linux, the same runner is the live full-suite parity gate in
`.github/workflows/live-linux-parity.yaml`. That workflow runs on trusted
`main` pushes or manual dispatch on a self-hosted runner labeled `linux`,
`x64`, and `kvm`, with KVM, `/dev/vhost-vsock`, `/dev/net/tun`, Firecracker,
and the network setup from `scripts/dev/microagent-e2e-linux-network-setup.sh`.

Those feature scenarios are backend-agnostic: the scenario names describe the
shared CLI/runtime contract, while the host or
`MICROAGENT_E2E_BACKEND=applevf` selects the Apple VF execution lane. Add the
targeted `applevf-*` scenarios when you need narrower diagnostics for boot,
direct console, substrate, workspace connect, network mode, TCP publish, or
vsock forwarding. Use `--keep` or `MICROAGENT_E2E_KEEP=1` only when you need to
preserve state directories for debugging; otherwise successful scenarios clean
up their own temporary state. If the local Docker config names a missing
credential helper, set `DOCKER_CONFIG` to an empty temporary directory for
public-image validation rather than editing host login state.

The Apple VF lane should cover portable CLI behavior, lifecycle/substrate,
connect/logs/ps, NAT/user/isolated/publish networking, mediation and generic
virtio-vsock behavior, supervision, quarantine cleanup, results, artifacts,
attached disks, and text/JSON output. Bridged mode is entitlement-gated:
open-source ad-hoc builds should fail closed with the
`com.apple.vm.networking` restriction named unless the supervisor is signed with
Apple's restricted entitlement.

Keep one-off run logs and investigation notes out of `docs/`; update the
Notion task or another tracker with run evidence instead.
