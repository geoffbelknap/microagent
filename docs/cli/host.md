---
title: microagent host
description: Report host backend capabilities.
---

```text
microagent host [--backend <name>] [--arch <arch>] [--supervisor <path>]
```

`host` reports what Microagent can see on the current machine: backend,
architecture, supervisor availability, kernel status, virtualization support,
vsock support, and console mode. It uses the same probes as
[`doctor`](/cli/doctor/), but is meant as an inspectable capability report.

## Flags

| Flag | Description |
|---|---|
| `--backend <name>` | Backend override (`apple-vf` or `firecracker`) |
| `--arch <arch>` | Guest architecture (`amd64`, `arm64`) |
| `--supervisor <path>` | Override the active backend supervisor path |
| `--json` | Print structured JSON output |

## Console Modes

| Backend | Console |
|---|---|
| Apple VF | `interactive` via [`connect`](/cli/connect/) |
| Firecracker | `interactive` via [`connect`](/cli/connect/); captured output via [`logs`](/cli/logs/) |

`consoleAvailable` reports backend capability on this host. A workspace can
still reject `connect` until it is running and the backend has created the
runtime console input endpoint.

## Example

```bash
microagent host
microagent host --json
```

## Related

- [`doctor`](/cli/doctor/)
- [Backends](/concepts/backends/)
- [`kernel verify`](/cli/kernel/)
