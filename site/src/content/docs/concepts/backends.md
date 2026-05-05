---
title: Backends
description: One backend per host OS. Same lifecycle surface, different mechanics.
---

Microagent has one backend per host OS. The `--backend` flag exists for
lower-level request compatibility and backend smoke tests, not as a user-facing
backend selector.

| Backend | Host OS | Implementation | `connect` | Process model |
|---|---|---|---|---|
| `firecracker` | Linux | In-process Go | not supported (use `logs`) | Microagent records VM PID; `stop` sends SIGTERM, `kill` sends SIGKILL |
| `apple-vf` | macOS | Swift supervisor (`microagent-applevf-supervisor`) | supported | One supervisor invocation per request |

Both backends expose the same lifecycle surface: `run`, `create`, `start`,
`status`, `stop`, `kill`, `delete`. Both record state files and emit
lifecycle events.

## Firecracker (Linux)

- Requires `/dev/kvm` and the `firecracker` binary on `PATH` (or under
  `<prefix>/libexec/firecracker`, or `MICROAGENT_FIRECRACKER`).
- `delete` refuses to remove state while the recorded VM process is still
  running. Use `stop` or `kill` first.
- Does not support interactive `connect`. Use [`logs`](/cli/logs/) for serial
  output.
- Default kernel SHA is pinned. See [Smoke tests](/operations/smoke-tests/).

## Apple VF (macOS)

- Uses Apple Virtualization.framework via the Swift supervisor.
- Supports interactive `connect` and `connect --send`.
- The supervisor is packaged as `microagent-applevf-supervisor`. Override
  with `--supervisor` or `MICROAGENT_APPLEVF_SUPERVISOR`.
- Default kernel for arm64 lives at
  `~/.microagent/kernels/apple-vf/arm64/Image`.

## Selecting a host

`microagent doctor` reports the active backend, the binary it found,
KVM/VF availability, and the default kernel status.
