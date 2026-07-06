---
title: microagent doctor
description: Check whether this host can boot microVMs, and why not.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-06_

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
Backend: linux-kvm
Status: ok
Host: amd64, supervisor=/usr/local/lib/microagent/firecracker-supervisor, supervisor available, virtualization supported, KVM available, vsock available
Console: available (interactive)
Kernel: installed (/home/user/.microagent/kernels/linux-kvm/amd64/vmlinux)
```

The `Networking:` line is backend-specific. Linux reports `isolated` and `user`
readiness, including whether `pasta`, unprivileged user namespaces, and
`/dev/net/tun` are present for `user` mode. Apple VF reports its local
`isolated` and `user` readiness.

`doctor` shares the structured shape with [`host`](/cli/host/): `microagent
--json doctor` returns the same `vmkit.Response` with `ok`, `backend`, `host`,
and `kernel` populated. `ok` is `false` when any required check fails.

## What it checks

- **Apple VF (macOS):** Virtualization.framework available, supervisor
  reachable, default kernel installed, interactive console available.
- **Firecracker (Linux):** `firecracker` binary on PATH (or
  `MICROAGENT_FIRECRACKER`), `/dev/kvm` present, `/dev/vhost-vsock` present,
  `/dev/net/tun` present, `pasta` available for user-mode networking,
  unprivileged user namespaces actually work the way boots use them (a live
  probe runs the same `unshare --map-root-user` setup as the supervisor jail
  and `pasta`, where the confined process writes its own uid map - so policy
  layers like AppArmor's `kernel.apparmor_restrict_unprivileged_userns`, which
  allow namespace creation but deny that self-write, are caught, not just the
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
| `--backend <name>` | Backend override (`apple-vf`, `linux-kvm`, or `windows-hyperv`) |
| `--arch <arch>` | Guest architecture (`amd64`, `arm64`) |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--json` | Global flag before `doctor`; print structured JSON output |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`/`--supervisor`.

## Exit status

`doctor` exits `0` when every required check passes; nonzero when any required
check fails. The printed summary still includes the full check detail either
way. In AX mode a failure is additionally written as a structured error
envelope.

## Related

- [Backends](/concepts/backends/) - what each backend requires
- [`host`](/cli/host/) - the same data as a capability report
- [`kernel install`](/cli/kernel/) - install the default kernel
