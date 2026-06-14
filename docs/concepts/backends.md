---
title: Backends
description: See what each host OS supports before you pick where to run microagent.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-13_

microagent installs with one backend per host OS: Firecracker on Linux,
Apple Virtualization.framework on macOS, and Hyper-V on Windows.
This page is the cross-platform picture - what each backend supports and what
it needs from the host - so you can decide which OS to deploy on and check
whether yours has what it needs. The CLI does not fall back to a cross-host
default - if a request names a backend that does not match the installed host
OS, microagent fails before it builds a rootfs or talks to a supervisor.

| Backend | Host OS | Maturity | Networking modes | Requirements | Notes |
|---|---|---|---|---|---|
| `firecracker` | Linux | Production | `user`, `nat`, `isolated`, `bridged`, `named`, TCP `--publish` | `/dev/kvm`, `firecracker` binary | Full feature surface; importable as a Go package |
| `apple-vf` | macOS (Apple silicon) | Production | `user`, `nat`, `isolated`, TCP `--publish`; `bridged` entitlement-gated | Virtualization.framework, Swift supervisor binary | NAT is macOS-managed; no `named` networks yet |
| `windows-hyperv` | Windows | Supported | `user`/`nat` (HNS NAT), `isolated`, `bridged` (named HNS network/switch), TCP `--publish` | Hyper-V / Host Compute Service | Linux guests without WSL or QEMU |

All three expose the same backend-neutral request and response structures, the
same lifecycle verbs, and interactive [`connect`](/cli/connect/) - the
mechanics under each verb differ per host. Firecracker and Apple VF share the
executable supervisor boundary; Windows Hyper-V speaks the same `vmkit`
protocol inside a Go supervisor boundary. Networking internals per backend are
covered in [Networking](/concepts/networking/).

## Firecracker (Linux)

- Uses `microagent-firecracker-supervisor` around the Firecracker process.
  Override the supervisor with `--supervisor` or
  `MICROAGENT_FIRECRACKER_SUPERVISOR`.
- Requires `/dev/kvm` and the `firecracker` binary on `PATH` (or under
  `<prefix>/libexec/firecracker`, or `MICROAGENT_FIRECRACKER`).
- `delete` refuses to remove state while the recorded VM process is still
  running. Use `stop` or `kill` first.
- Supports interactive [`connect`](/cli/connect/) and `connect --send`. Use
  [`logs`](/cli/logs/) when you only need captured serial output.
- The default kernel path is `~/.microagent/kernels/firecracker/<arch>/Image`.

## Apple VF (macOS)

- Uses Apple Virtualization.framework via the Swift executable supervisor,
  packaged as `microagent-applevf-supervisor`. The supervisor runs one
  invocation per request - there is no resident daemon process on macOS.
  Override with `--supervisor` or `MICROAGENT_APPLEVF_SUPERVISOR`.
- Supports interactive `connect` and `connect --send`.
- Supports `user`, `nat`, `isolated`, and TCP `--publish` (`user` and `nat`
  both use Apple's native NAT attachment). Native bridged networking
  is implemented, but public builds fail closed because Apple gates it behind
  the restricted `com.apple.vm.networking` entitlement.
- The default arm64 kernel lives at
  `~/.microagent/kernels/apple-vf/arm64/Image`.

## Windows Hyper-V

- Targets Linux microVM-style workspaces on Windows without WSL or QEMU.
- Uses the backend name `windows-hyperv` and Host Compute Service through
  `vmcompute.dll`; lifecycle state records HCS compute IDs.
- Consumes VHD root disks at `~/.microagent/workspaces/<name>/rootfs.vhd`, and
  wraps named volumes as VHD-backed ext4 disks.
- Supports the full lifecycle: `host`, `check`, `prepare`, `run`, `start`,
  `inspect`, `connect`, `halt`, `quarantine`, `stop`, `kill`, and `delete`.
  `clone`, `cp`, artifacts, and `commit` ride guest-mediated maintenance boots.
- Supports `user`/`nat` (HNS NAT) and `isolated` networking plus published TCP
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
  details and current limitations (bridged mode attaches to a named HNS network
  or Hyper-V switch; pause/resume and named networks are supported; snapshots
  are not supported — HCS-direct VMs have no guest-memory save-state).

## Checking your host

`microagent doctor` reports the active backend, backend-specific host support,
virtualization availability, guest-init availability, and the default kernel
status. Run it before anything else on a new machine.

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

On Windows, the same runner works under Git Bash with the windows-hyperv
backend (Hyper-V role active, HCS services running): the portable scenarios
plus the `windows-hyperv-*-host` probes — which wrap the gated Go smokes for
boot/result, connect, structured exec, and mediation — run in the
`live-windows-hyperv` workflow. Backend-neutral feature scenarios that have
not joined the Windows lane yet self-skip with the lane named.

The feature scenarios are backend-agnostic: the scenario names describe the
shared CLI/runtime contract, while the host or
`MICROAGENT_E2E_BACKEND=applevf` selects the Apple VF execution lane. Add the
targeted `applevf-*` scenarios when you need narrower diagnostics for boot,
direct console, substrate, workspace connect, network mode, TCP publish, or
vsock forwarding. Use `--keep` or `MICROAGENT_E2E_KEEP=1` only when you need to
preserve state directories for debugging; successful scenarios clean up their
own temporary state. If the local Docker config names a missing credential
helper, set `DOCKER_CONFIG` to an empty temporary directory for public-image
validation rather than editing host login state.

The Apple VF lane should cover portable CLI behavior, lifecycle/substrate,
connect/logs/ps, NAT/user/isolated/publish networking, mediation and generic
virtio-vsock behavior, supervision, quarantine cleanup, results, artifacts,
attached disks, and text/JSON output. Bridged mode is entitlement-gated:
open-source ad-hoc builds should fail closed with the
`com.apple.vm.networking` restriction named unless the supervisor is signed
with Apple's restricted entitlement.

Keep one-off run logs and investigation notes out of `docs/`; update an
external tracker with run evidence instead.
