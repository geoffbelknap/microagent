---
title: microagent host
description: Report host backend capabilities.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-30_

```text
microagent host [--arch <arch>] [--supervisor <path>]   Report host backend capabilities
```

`host` reports what `microagent` can see on the current machine: backend,
architecture, supervisor availability, kernel status, virtualization support,
vsock support, and console mode. It uses the same probes as
[`doctor`](doctor.md), but is meant as an inspectable capability report
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
| Apple VF | `interactive` via [`connect`](connect.md) |
| Firecracker | `interactive` via [`connect`](connect.md); captured output via [`logs`](logs.md) |

`consoleAvailable` reports backend capability on this host. A workspace can
still reject `connect` until it is running and the backend has created the
runtime console input endpoint.

## Flags

Most runs need no flags. `host` reports the backend this install shipped
with; use `--arch` when you plan to run non-native guests.

| Flag | Description |
|---|---|
| `--arch <arch>` | Guest architecture (`amd64`, `arm64`) |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](index.md#global-flags) for `--output`/`--json`/`--supervisor`.

## Exit status

`host` exits `0` when the capability report is produced; nonzero when the
probes cannot run.

## Related

- [`doctor`](doctor.md) - the same probes as a pass/fail health check
- [Backends](../concepts/backends.md) - what each backend requires
- [Networking](../concepts/networking.md) - the available network modes
- [`kernel verify`](kernel.md) - check the kernel the report points at
