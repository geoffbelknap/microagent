---
title: microagent doctor
description: Check that the host can run microagent.
---

```text
microagent doctor [--arch <arch>] [--supervisor <path>]
```

`doctor` reports host support for the installed host backend and the default kernel
status. Run it first when something isn't working.

Use [`host`](/cli/host/) when you want the same information as an inspectable
capability report rather than a health check.

## What it checks

- **Apple VF (macOS):** Virtualization.framework available, supervisor
  reachable, default kernel installed, interactive console available.
- **Firecracker (Linux):** `firecracker` binary on PATH (or
  `MICROAGENT_FIRECRACKER`), `/dev/kvm` present, `/dev/vhost-vsock` present,
  `/dev/net/tun` present, `pasta` available for user-mode networking,
  unprivileged user namespaces enabled, default kernel installed, interactive
  console available.
- **Windows Hyper-V (experimental):** Windows Host Compute Service available,
  Hyper-V / Windows Hypervisor Platform support available, HCS access allowed
  for the current user, default kernel installed, guest-init available, and
  interactive console reported as unsupported.

On Linux, run `microagent doctor` outside sandboxed agent environments so KVM
visibility is honest.

## Flags

| Flag | Description |
|---|---|
| `--backend <name>` | Backend override (`apple-vf`, `firecracker`, or `windows-hyperv`) |
| `--arch <arch>` | Guest architecture (`amd64`, `arm64`) |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--json` | Global flag before `doctor`; print structured JSON output |

## Example

```bash
microagent doctor
microagent --json doctor
```

## Related

- [Backends](/concepts/backends/)
- [`host`](/cli/host/)
- [`kernel install`](/cli/kernel/)
