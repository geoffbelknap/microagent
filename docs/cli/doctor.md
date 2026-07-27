---
title: microagent doctor
description: Check whether this host can boot microVMs, and why not.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-27_

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
Confinement: rootless (active)
Networking: isolated ready, user ready
Egress TPROXY modules: PASS
Capabilities: PASS (5/5 ready)
Kernel: installed (/home/user/.microagent/kernels/linux-kvm/amd64/Image)
```

The `Confinement:` line reports the host VMM-process confinement posture
(`off`, `jailer`, or `rootless`) and whether it is active.

The `Networking:` line is backend-specific. Linux reports `isolated` and `user`
readiness, including whether `pasta`, unprivileged user namespaces, and
`/dev/net/tun` are present for `user` mode. Apple VF reports its local
`isolated` and `user` readiness.

The `Capabilities:` line reports the L1 (prerequisites-verified) status of each
capability the backend declares — whether the host-side preconditions for
structured exec, live network apply, snapshot, broker endpoints, and the
interactive console are present.
It is a prerequisite check, not operational proof: L1 does not boot a workspace
or take a real snapshot. A capability that is not ready lists the missing
prerequisites. The structured `--json` output carries the full per-capability
matrix under `host.capabilities`.

`doctor` shares the structured shape with [`host`](/cli/host/): `microagent
--json doctor` returns the same `vmkit.Response` with `ok`, `backend`, `host`,
and `kernel` populated. `ok` is `false` when any required check fails.

## What it checks

- **Apple VF (macOS):** Virtualization.framework available, supervisor
  reachable, save/restore support for snapshots (macOS 14+), default kernel
  installed, interactive console available.
- **Firecracker (Linux):** `firecracker` binary on PATH (or
  `MICROAGENT_FIRECRACKER`), `/dev/kvm` present, `/dev/vhost-vsock` present,
  `/dev/net/tun` present, `pasta` available for user-mode networking,
  unprivileged user namespaces actually work the way boots use them (a live
  probe that also catches AppArmor userns restrictions - see
  [troubleshooting](/troubleshooting/)), default kernel installed, interactive
  console available.
On Linux, run `microagent doctor` outside sandboxed agent environments so KVM
visibility is honest.

## Flags

You'll rarely need flags here - `--backend` to probe a backend other than the
detected one, `--arch` when you plan to run non-native guests.

| Flag | Description |
|---|---|
| `--backend <name>` | Backend override (`apple-vf` or `linux-kvm`) |
| `--arch <arch>` | Guest architecture (`amd64`, `arm64`) |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--json` | Global flag before `doctor`; print structured JSON output |

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--supervisor`.

## Exit status

The `Status:` line is a three-way verdict, and the exit code follows it:

- `ok` (exit `0`) — every check passed.
- `degraded` (exit `0`) — microVMs boot today (supervisor, virtualization,
  KVM, VMM binary, guest-init, and kernel are all present) but something
  optional is missing. The missing pieces print as `Warnings:` directly under
  the status.
- `failed` (exit `1`) — the core boot path itself is broken; no run can work.
  The root cause prints as `Error:` directly under the status, above the
  check detail it explains.

The printed summary includes the full check detail in every case.

## Related

- [Backends](/concepts/backends/) - what each backend requires
- [`host`](/cli/host/) - the same data as a capability report
- [`kernel install`](/cli/kernel/) - install the default kernel
