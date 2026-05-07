---
title: microagent doctor
description: Check that the host can run microagent.
---

```text
microagent doctor [--backend <name>] [--arch <arch>] [--supervisor <path>]
```

`doctor` reports host support for the active backend and the default kernel
status. Run it first when something isn't working.

Use [`host`](host.md) when you want the same information as an inspectable
capability report rather than a health check.

## What it checks

- **Apple VF (macOS):** Virtualization.framework available, supervisor
  reachable, default kernel installed, interactive console available.
- **Firecracker (Linux):** `firecracker` binary on PATH (or
  `MICROAGENT_FIRECRACKER`), `/dev/kvm` present, `/dev/vhost-vsock` present,
  default kernel installed, interactive console available.

On Linux, run `microagent doctor` outside sandboxed agent environments so KVM
visibility is honest.

## Flags

| Flag | Description |
|---|---|
| `--backend <name>` | Backend override (`apple-vf` or `firecracker`) |
| `--arch <arch>` | Guest architecture (`amd64`, `arm64`) |
| `--supervisor <path>` | Override the Apple VF supervisor path |
| `--json` | Global flag before `doctor`; print structured JSON output |

## Example

```bash
microagent doctor
microagent --json doctor
```

## Related

- [Backends](../concepts/backends.md)
- [`host`](host.md)
- [`kernel install`](kernel.md)
