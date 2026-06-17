---
title: microagent host
description: Report host backend capabilities.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-16_

```text
microagent host [--arch <arch>] [--supervisor <path>]   Report host backend capabilities
microagent host setup-networking [--check | --revert]   Prepare privileged network modes (Linux)
```

`host` reports what `microagent` can see on the current machine: backend,
architecture, supervisor availability, kernel status, virtualization support,
vsock support, and console mode. It uses the same probes as
[`doctor`](/cli/doctor/), but is meant as an inspectable capability report
rather than a health check. `host setup-networking` prepares a Linux host for
the privileged network modes.

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

## `setup-networking`

On Linux, the `nat` and `named` network modes need the host to have
IPv4 forwarding enabled and the Firecracker supervisor to hold `CAP_NET_ADMIN`.
`isolated` and `user` (passt) modes work without any setup. This subcommand
prepares the host for the privileged modes; run [`doctor`](/cli/doctor/) to see
which modes are currently available. (The unsupported `bridged` mode shares the
same `CAP_NET_ADMIN` requirement.)

Enable the privileged network modes (it mutates host state):

```bash
microagent host setup-networking
```

> Run it as your normal user — it explains the change, asks for confirmation, then re-runs itself under `sudo` (so it works regardless of where Homebrew installed the binary). Pass `--yes` to skip the prompt; on a non-interactive shell it prints the exact `sudo` command instead of elevating.

It persists `net.ipv4.ip_forward=1` (via `/etc/sysctl.d/99-microagent.conf`) and
runs `setcap cap_net_admin+eip` on the installed supervisor. A `brew upgrade`
reinstalls the supervisor and clears the capability, so re-run it after
upgrading.

| Flag | Description |
|---|---|
| `--check` | Report readiness without changing the host (no root needed); nonzero exit if not ready |
| `--revert` | Remove the sysctl drop-in and drop the supervisor capability |
| `--yes` | Skip the confirmation prompt before re-running under `sudo` |

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
| `--backend <name>` | Backend override (`apple-vf`, `firecracker`, or `windows-hyperv`) |
| `--arch <arch>` | Guest architecture (`amd64`, `arm64`) |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`/`--supervisor`.

## Exit status

`host` exits `0` when the capability report is produced; nonzero when the
probes cannot run. `host setup-networking --check` exits nonzero when the host
is not ready for the privileged network modes. In AX mode a failure is written
as a structured error envelope.

## Related

- [`doctor`](/cli/doctor/) - the same probes as a pass/fail health check
- [Backends](/concepts/backends/) - what each backend requires
- [Networking](/concepts/networking/) - what the privileged modes need
- [`kernel verify`](/cli/kernel/) - check the kernel the report points at
