---
title: Table Stakes Feature Readiness
description: Readiness record for the first microagent-kit table-stakes feature pass.
---

This note records the feature state after the Firecracker Linux parity pass and
the local Apple VF validation pass. The final validated revision was:

```text
branch=main
sha=fa860de4b06948a77809c4a757ec74dc52ace4d8
```

## Apple VF Host

```text
product=macOS 26.4.1 25E253
kernel=Darwin 25.4.0 RELEASE_ARM64_T8142
arch=arm64
backend=apple-vf
console=interactive
```

## Apple VF Validation

| Gate | Result |
|---|---|
| `make smoke` | pass |
| `make smoke-boot` | pass |
| `make smoke-applevf-network` | pass; `nat`, `isolated`, and Apple VF `--publish` accepted, bridged is entitlement-gated |
| `make smoke-applevf-publish` | pass; TCP and BusyBox HTTP publish |
| `MICROAGENT_APPLEVF_BOOT_NETWORK_MODE=isolated scripts/applevf-boot-smoke.sh` | pass |
| `microagent perf boot` | one successful iteration, `1952 ms` |
| `microagent perf footprint perf-steady-applevf` | `20080 KiB` RSS |
| `microagent perf steady perf-steady-applevf --duration 5 --interval 1` | min/avg/max `20080/20080/20080 KiB` RSS |

The perf commands used a locally built CLI and guest init under
`/private/tmp/microagent-kit-perf`, the repository-built Apple VF supervisor,
the installed Apple VF arm64 kernel, and the `small` profile.

## Firecracker Validation

Firecracker Linux parity was validated separately from
`8dbdcf84fd833620b90a8a8e530cc580681e0928` to
`fa860de4b06948a77809c4a757ec74dc52ace4d8`. The full earlier record is in
[`firecracker-linux-parity-readiness.md`](./firecracker-linux-parity-readiness.md).

```text
distro=Ubuntu 24.04.4 LTS
kernel=6.8.0-110-generic
arch=x86_64
/dev/kvm=present
/dev/vhost-vsock=present
firecracker=Firecracker v1.15.1
```

Validated Linux gates:

| Gate | Result |
|---|---|
| `scripts/go-test.sh` | pass |
| `scripts/firecracker-console-parity-smoke.sh` | pass |
| `scripts/firecracker-publish-smoke.sh` | pass |
| `scripts/firecracker-network-mode-smoke.sh` | pass; `nat` boots, `isolated` boots without guest `eth0`, isolated `--publish` fails closed, bridged reports `host-prerequisite-not-configured` without a configured Linux bridge |
| `scripts/firecracker-workspace-smoke.sh` | pass |
| `scripts/firecracker-boot-smoke.sh` | pass |
| `make smoke` | pass |

The Linux handoff helper is intentionally absent from `main`; it was removed
from the public repository in `ba9b4a1`.

Recorded Firecracker performance:

| Command | Result |
|---|---|
| `microagent perf boot` | one successful iteration, `18746 ms` |
| `microagent perf footprint perf-main-20260506` | `67736 KiB` RSS |
| `microagent perf steady perf-main-20260506 --duration 5 --interval 1` | 6 samples, min/avg/max `67736/67736/67736 KiB` RSS |

## Feature Matrix

| Feature | Apple VF | Firecracker | State |
|---|---|---|---|
| Networking | `nat`, TCP `--publish`, `isolated`, and entitlement-gated explicit-interface `bridged` | `nat` plus live TCP `--publish`; `isolated`; Linux-bridge-backed `bridged` with transient TAP setup | Ready, with Apple bridged unsupported in public builds |
| File transfer | `microagent cp` in/out of stopped rootfs and attached disks | same CLI semantics | Ready |
| Console ergonomics | interactive `connect`, `--send`, readiness errors, clean detach | interactive `connect`, `--send`, readiness errors, clean detach | Ready, except resize |
| Cloning | stopped workspace/template clone | stopped workspace/template clone | Ready |
| Restart policy | declarative `never`, `on-failure`, `always` state | declarative `never`, `on-failure`, `always` state | Ready |
| Resource management | named profiles and exact resource config | named profiles and exact resource config | Ready |
| Diagnostics | `doctor` and `host` report backend, arch, virtualization, supervisor, kernel, vsock, console capability | same, with KVM/vhost-vsock and Firecracker binary checks | Ready |
| Image management | pull, tag, list, remove, prune, OCI rootfs build, templates | same CLI surface | Ready |
| Declarative spec | `microagent.yaml` covers image, resources, restart, network intent, mounts, setup, publish declarations | same parsing and validation | Ready |
| Measured performance | local boot, footprint, steady numbers recorded | Linux boot, footprint, steady numbers recorded | Ready |

## Post-Release Follow-Ups

- Apple VF bridged networking is implemented behind Apple's restricted
  `com.apple.vm.networking` entitlement. Public open-source builds should report
  it as unsupported because Apple does not allow projects to self-sign that
  entitlement.
- Add terminal resize propagation for interactive console sessions. The current
  parity target is attach, send, readiness, detach, and logs.
- Consider a future schema split between host console capability and per-runtime
  console input readiness if `consoleAvailable` becomes ambiguous for callers.
