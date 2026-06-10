---
title: microagent host
description: Report host backend capabilities.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-03_

```text
microagent host [--arch <arch>] [--supervisor <path>]
```

`host` reports what `microagent` can see on the current machine: backend,
architecture, supervisor availability, kernel status, virtualization support,
vsock support, and console mode. It uses the same probes as
[`doctor`](/cli/doctor/), but is meant as an inspectable capability report.

## Flags

| Flag | Description |
|---|---|
| `--backend <name>` | Backend override (`apple-vf`, `firecracker`, or `windows-hyperv`) |
| `--arch <arch>` | Guest architecture (`amd64`, `arm64`) |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--json` | Global flag before `host`; print structured JSON output |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`/`--supervisor`.

## `setup-networking`

```text
microagent host setup-networking [--check | --revert]
```

On Linux, the `nat`, `bridged`, and `named` network modes need the host to have
IPv4 forwarding enabled and the Firecracker supervisor to hold `CAP_NET_ADMIN`.
`isolated` and `user` (passt) modes work without any setup. This subcommand
prepares the host for the privileged modes; run [`doctor`](/cli/doctor/) to see
which modes are currently available.

Run as root (it mutates host state):

```bash
sudo microagent host setup-networking
```

It persists `net.ipv4.ip_forward=1` (via `/etc/sysctl.d/99-microagent.conf`) and
runs `setcap cap_net_admin+eip` on the installed supervisor. A `brew upgrade`
reinstalls the supervisor and clears the capability, so re-run it after
upgrading.

| Flag | Description |
|---|---|
| `--check` | Report readiness without changing the host (no root needed); non-zero exit if not ready |
| `--revert` | Remove the sysctl drop-in and drop the supervisor capability |

## Console modes

| Backend | Console |
|---|---|
| Apple VF | `interactive` via [`connect`](/cli/connect/) |
| Firecracker | `interactive` via [`connect`](/cli/connect/); captured output via [`logs`](/cli/logs/) |
| Windows Hyper-V | `hvsock` via [`connect`](/cli/connect/); captured output via [`logs`](/cli/logs/) |

`consoleAvailable` reports backend capability on this host. A workspace can
still reject `connect` until it is running and the backend has created the
runtime console input endpoint.

## Example

```bash
microagent host
microagent --json host
```

The `--json` report carries the capability probes under `host` (and the default
kernel under `kernel`). A trimmed Firecracker example:

```json
{
  "ok": true,
  "backend": "firecracker",
  "host": {
    "backend": "firecracker",
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
    "backend": "firecracker",
    "architecture": "amd64",
    "status": "installed",
    "path": "/home/user/.microagent/kernels/firecracker/amd64/vmlinux",
    "sha256": "..."
  }
}
```

## Related

- [`doctor`](/cli/doctor/)
- [Backends](/concepts/backends/)
- [`kernel verify`](/cli/kernel/)
