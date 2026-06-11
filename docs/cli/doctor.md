---
title: microagent doctor
description: Check whether this host can boot microVMs, and why not.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

```text
microagent doctor [--arch <arch>] [--supervisor <path>]
```

`doctor` reports host support for the installed host backend and the default
kernel status. Run it first when something isn't working. Use
[`host`](/cli/host/) when you want the same information as an inspectable
capability report rather than a health check.

## Examples

Check the host:

```bash
microagent doctor
microagent --json doctor
```

Text output is a short health summary:

```text
Backend: firecracker
Status: ok
Host: amd64, supervisor=/usr/local/lib/microagent/firecracker-supervisor, supervisor available, virtualization supported, KVM available, vsock available
Console: available (interactive)
Kernel: installed (/home/user/.microagent/kernels/firecracker/amd64/vmlinux)
```

`doctor` shares the structured shape with [`host`](/cli/host/): `microagent
--json doctor` returns the same `vmkit.Response` with `ok`, `backend`, `host`,
and `kernel` populated. `ok` is `false` when any required check fails.

## What it checks

- **Apple VF (macOS):** Virtualization.framework available, supervisor
  reachable, default kernel installed, interactive console available.
- **Firecracker (Linux):** `firecracker` binary on PATH (or
  `MICROAGENT_FIRECRACKER`), `/dev/kvm` present, `/dev/vhost-vsock` present,
  `/dev/net/tun` present, `pasta` available for user-mode networking,
  unprivileged user namespace creation actually works (a live `CLONE_NEWUSER`
  probe, so policy layers like AppArmor's
  `kernel.apparmor_restrict_unprivileged_userns` are caught, not just the
  classic userns sysctls), default kernel installed, interactive console
  available.
- **Windows Hyper-V (experimental):** Windows Host Compute Service available,
  Hyper-V / Windows Hypervisor Platform support available, HCS access allowed
  for the current user, HCN/HNS networking available, Hyper-V sockets
  available, default kernel installed, guest-init available, and HVSock console
  support available.

On Linux, run `microagent doctor` outside sandboxed agent environments so KVM
visibility is honest.
On Windows, run it from the same user account that will start workspaces. HCS
access usually requires Administrator or membership in the Hyper-V
Administrators group.

## Flags

You'll rarely need flags here - `--backend` to probe a backend other than the
detected one, `--arch` when you plan to run non-native guests.

| Flag | Description |
|---|---|
| `--backend <name>` | Backend override (`apple-vf`, `firecracker`, or `windows-hyperv`) |
| `--arch <arch>` | Guest architecture (`amd64`, `arm64`) |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--json` | Global flag before `doctor`; print structured JSON output |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`/`--supervisor`.

## Exit status

`doctor` exits `0` when every required check passes; nonzero when any required
check fails. The printed summary still includes the full check detail either
way.

## Related

- [Backends](/concepts/backends/)
- [`host`](/cli/host/)
- [`kernel install`](/cli/kernel/)
