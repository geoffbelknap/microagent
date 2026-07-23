---
title: microagent host
description: Report host backend capabilities.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-23_

```text
microagent host [--arch <arch>] [--supervisor <path>]   Report host backend capabilities
```

`host` reports what `microagent` can see on the current machine: backend,
architecture, supervisor availability, kernel status, virtualization support,
vsock support, and console mode. It uses the same probes as
[`doctor`](/cli/doctor/), but is meant as an inspectable capability report
rather than a health check.

## Examples

Inspect the host:

```bash
microagent host
microagent --json host
```

The `--json` report carries the capability probes under `host` (and the default
kernel under `kernel`). A trimmed Firecracker example:

```json
{
  "ok": true,
  "backend": "linux-kvm",
  "host": {
    "backend": "linux-kvm",
    "architecture": "amd64",
    "supervisorPath": "/usr/local/lib/microagent/firecracker-supervisor",
    "supervisorAvailable": true,
    "kvmAvailable": true,
    "vsockAvailable": true,
    "tunAvailable": true,
    "userNetworkingAvailable": true,
    "userNamespacesAvailable": true,
    "consoleAvailable": true,
    "consoleMode": "interactive"
  },
  "kernel": {
    "backend": "linux-kvm",
    "architecture": "amd64",
    "status": "installed",
    "path": "/home/user/.microagent/kernels/linux-kvm/amd64/Image",
    "sha256": "..."
  }
}
```

## Console modes

| Backend | Console |
|---|---|
| Apple VF | `interactive` via [`connect`](/cli/connect/) |
| Firecracker | `interactive` via [`connect`](/cli/connect/); captured output via [`logs`](/cli/logs/) |
| Windows Hyper-V | `hvsock` via [`connect`](/cli/connect/); captured output via [`logs`](/cli/logs/) |

`consoleAvailable` reports backend capability on this host. A workspace can
still reject `connect` until it is running and the backend has created the
runtime console input endpoint.

## Flags

You'll rarely need flags here - `--backend` to probe a backend other than the
detected one, `--arch` when you plan to run non-native guests.

| Flag | Description |
|---|---|
| `--backend <name>` | Backend override (`apple-vf`, `linux-kvm`, or `windows-hyperv`) |
| `--arch <arch>` | Guest architecture (`amd64`, `arm64`) |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--mode`/`--supervisor`.

## Exit status

`host` exits `0` when the capability report is produced; nonzero when the
probes cannot run. In AX mode a failure is written as a structured error
envelope.

## Related

- [`doctor`](/cli/doctor/) - the same probes as a pass/fail health check
- [Backends](/concepts/backends/) - what each backend requires
- [Networking](/concepts/networking/) - the available network modes
- [`kernel verify`](/cli/kernel/) - check the kernel the report points at
