---
title: Backends
description: One backend per host OS. Same lifecycle surface, different mechanics.
---

microagent-kit uses one backend per host OS. The `--backend` flag exists for
lower-level request compatibility and backend validation, not for normal
backend selection.

| Backend | Host OS | Supervisor | `connect` | Process model |
|---|---|---|---|---|
| `firecracker` | Linux | Go executable supervisor (`microagent-firecracker-supervisor`) | supported | Supervisor records VM PID; `quarantine` preserves it, `stop` sends SIGTERM, `kill` sends SIGKILL |
| `apple-vf` | macOS | Swift executable supervisor (`microagent-applevf-supervisor`) | supported | One supervisor invocation per request |

Both backends expose the same lifecycle surface: `run`, `create`, `start`,
`status`, `halt`, `quarantine`, `stop`, `kill`, `delete`. Both record state
files and emit lifecycle events. Firecracker and Apple VF share the same
executable supervisor-shaped request/response boundary.

## Firecracker (Linux)

- Uses `microagent-firecracker-supervisor` around the Firecracker process.
- Override the supervisor with `--supervisor` or
  `MICROAGENT_FIRECRACKER_SUPERVISOR`.
- Requires `/dev/kvm` and the `firecracker` binary on `PATH` (or under
  `<prefix>/libexec/firecracker`, or `MICROAGENT_FIRECRACKER`).
- `delete` refuses to remove state while the recorded VM process is still
  running. Use `stop` or `kill` first.
- Supports interactive `connect` and `connect --send`. Use
  [`logs`](../cli/logs.md) when you only need captured serial output.
- Default kernel SHA is pinned and checked by the smoke targets in the root
  `Makefile`.

## Apple VF (macOS)

- Uses Apple Virtualization.framework via the Swift executable supervisor.
- Supports interactive `connect` and `connect --send`.
- Supports `nat`, `isolated`, and TCP `--publish`. Native bridged networking
  is implemented, but public builds fail closed because Apple gates it behind
  the restricted `com.apple.vm.networking` entitlement.
- The supervisor is packaged as `microagent-applevf-supervisor`. Override with
  `--supervisor` or `MICROAGENT_APPLEVF_SUPERVISOR`.
- Default kernel for arm64 lives at
  `~/.microagent/kernels/apple-vf/arm64/Image`.

## Selecting a host

`microagent doctor` reports the active backend, the binary it found,
KVM/VF availability, and the default kernel status.
