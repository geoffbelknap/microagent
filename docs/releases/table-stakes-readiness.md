---
title: Table Stakes Feature Readiness
description: Readiness record for the first microagent-kit table-stakes feature pass.
---

This note records the feature state after the Firecracker Linux parity pass and
the local Apple VF validation pass. The validated base before this note was:

```text
branch=main
sha=ba9b4a1b8037a12dc45c7280958fe9f4d6efba24
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
| `make smoke-applevf-network` | pass; `nat` and `isolated` accepted, Apple VF `--publish` fails closed, bridged is entitlement-gated |
| `MICROAGENT_APPLEVF_BOOT_NETWORK_MODE=isolated scripts/applevf-boot-smoke.sh` | pass |
| `microagent perf boot` | one successful iteration, `1952 ms` |
| `microagent perf footprint perf-steady-applevf` | `20080 KiB` RSS |
| `microagent perf steady perf-steady-applevf --duration 5 --interval 1` | min/avg/max `20080/20080/20080 KiB` RSS |

The perf commands used a locally built CLI and guest init under
`/private/tmp/microagent-kit-perf`, the repository-built Apple VF supervisor,
the installed Apple VF arm64 kernel, and the `small` profile.

## Firecracker Validation

Firecracker Linux parity was validated separately on Ubuntu 24.04.4 LTS with
Firecracker v1.15.1. The full record is in
[`firecracker-linux-parity-readiness.md`](./firecracker-linux-parity-readiness.md).

Validated Linux gates:

| Gate | Result |
|---|---|
| `scripts/go-test.sh` | pass |
| `scripts/firecracker-console-parity-smoke.sh` | pass |
| `scripts/firecracker-publish-smoke.sh` | pass |
| `scripts/firecracker-network-mode-smoke.sh` | pass; `nat` boots, `isolated` boots without guest `eth0`, isolated `--publish` fails closed, bridged fails closed without a configured Linux bridge |
| `scripts/firecracker-workspace-smoke.sh` | pass |
| `scripts/firecracker-boot-smoke.sh` | pass |
| `make smoke` | pass |

Recorded Firecracker performance:

| Command | Result |
|---|---|
| `microagent perf boot` | one successful iteration, `18693 ms` |
| `microagent perf footprint perf-steady3` | `67640 KiB` RSS |
| `microagent perf steady perf-steady3 --duration 5 --interval 1` | min/avg/max `67640/67640/67640 KiB` RSS |

## Feature Matrix

| Feature | Apple VF | Firecracker | State |
|---|---|---|---|
| Networking | `nat`, `isolated`, and entitlement-gated explicit-interface `bridged`; `--publish` fails closed | `nat` plus live TCP `--publish`; `isolated`; Linux-bridge-backed `bridged` with transient TAP setup | Partial |
| File transfer | `microagent cp` in/out of stopped rootfs and attached disks | same CLI semantics | Ready |
| Console ergonomics | interactive `connect`, `--send`, readiness errors, clean detach | interactive `connect`, `--send`, readiness errors, clean detach | Ready, except resize |
| Cloning | stopped workspace/template clone | stopped workspace/template clone | Ready |
| Restart policy | declarative `never`, `on-failure`, `always` state | declarative `never`, `on-failure`, `always` state | Ready |
| Resource management | named profiles and exact resource config | named profiles and exact resource config | Ready |
| Diagnostics | `doctor` and `host` report backend, arch, virtualization, supervisor, kernel, vsock, console capability | same, with KVM/vhost-vsock and Firecracker binary checks | Ready |
| Image management | pull, tag, list, remove, prune, OCI rootfs build, templates | same CLI surface | Ready |
| Declarative spec | `microagent.yaml` covers image, resources, restart, network intent, mounts, setup, publish declarations | same parsing and validation | Ready, except backend networking gaps |
| Measured performance | local boot, footprint, steady numbers recorded | Linux boot, footprint, steady numbers recorded | Ready |

## Remaining Work

- Validate Firecracker network mode smoke on the Linux KVM host and record the
  final SHA/results for this slice.
- Implement Apple VF `--publish`.
- Decide release signing/provisioning or public unsupported status for Apple VF
  bridged networking. Apple gates it behind the restricted
  `com.apple.vm.networking` entitlement, which open-source builds cannot
  self-sign.
- Add terminal resize propagation for interactive console sessions. The current
  parity target is attach, send, readiness, detach, and logs.
- Consider a future schema split between host console capability and per-runtime
  console input readiness if `consoleAvailable` becomes ambiguous for callers.
