---
title: microagent doctor
description: Check whether this host can boot microVMs, and why not.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-15_

```text
microagent doctor [--arch <arch>] [--supervisor <path>] [--state-dir <dir>]
```

`doctor` reports host support for the installed host backend and the default
kernel status. Run it first when something isn't working. Use
[`host`](host.md) when you want the same information as an inspectable
capability report rather than a health check.

In a terminal, an individual host probe appears on stderr only when it crosses
the short-operation threshold. Normal fast checks do not flash a spinner, and
the final diagnosis remains the command's stdout result. JSON output contains
no terminal progress text.

## Examples

Check the host:

```bash
microagent doctor
microagent --json doctor
```

Text output is one check per line, then a verdict:

```text
Host: linux-kvm on amd64

  virtualization    ✓ KVM
  vmm               ✓ Firecracker v1.15.1
  supervisor        ✓
  guest init        ✓
  kernel            ✓ installed
  vsock             ✓
  networking        ✓ isolated, user
  confinement       ✓ active (rootless)
  structured exec   ✓
  port publish      ✓
  live port apply   ✓
  file copy         ✓ offline
  pause/resume      ✓
  snapshot create   ✓
  snapshot restore  ✓
  snapshot fork     ✓
  secret broker     ✓
  console           ✓ interactive
  egress mediation  ⚠ missing: kernel module xt_socket, kernel module nf_socket_ipv4
                    UDP egress mediation needs TPROXY kernel modules; load them (e.g. `modprobe nft_tproxy`) or build them into the kernel

Workspaces will boot and run on this host, but not everything is ready: egress mediation. Whatever needs a missing capability fails closed until it is fixed.
```

Each line is one verified check: `✓` ready, `⚠` degraded but usable, `✗` not
usable. A failing check prints its own line with what is missing and, when
there is one, the command that fixes it. The closing sentence is the verdict:
it states what will work on this host and what will not.

The `confinement` line reports the host VMM-process confinement posture
(`off`, `jailer`, `rootless`, or `seatbelt` on macOS) and whether it is
active. The `networking` line reports `isolated` and `user` mode readiness;
on Linux that covers `pasta`, unprivileged user namespaces, and
`/dev/net/tun`.

The capability lines (structured exec through egress mediation) are the L1
(prerequisites-verified) status of every capability the backend declares. It
is a prerequisite check, not operational proof: L1 does not boot a workspace
or take a real snapshot. A capability that is not ready lists the missing
prerequisites.

`doctor` shares the structured shape with [`host`](host.md): `microagent
--json doctor` returns the same `vmkit.Response` with `ok`, `verdict`,
`backend`, `host`, and `kernel` populated. `ok` is `false` when any probe
reported an issue; `verdict` is the rollup the text page prints (see "Exit
status"). The per-capability matrix, including each capability's `tier` and
missing prerequisites, is under `host.capabilities`.

## What it checks

- **Apple VF (macOS):** Virtualization.framework available, supervisor
  reachable, guest init installed, save/restore support for snapshots (macOS
  14+), default kernel installed, interactive console available.
- **Firecracker (Linux):** `firecracker` binary on PATH (or
  `MICROAGENT_FIRECRACKER`), `/dev/kvm` present, `/dev/vhost-vsock` present,
  `/dev/net/tun` present, `pasta` available for user-mode networking,
  unprivileged user namespaces actually work the way boots use them (a live
  probe that also catches AppArmor userns restrictions - see
  [troubleshooting](../troubleshooting.md)), kernel TPROXY support for mediated
  UDP verified by installing a probe steering rule in a scratch network
  namespace, default kernel installed, interactive console available.

On Linux, run `microagent doctor` outside sandboxed agent environments so
the KVM probe sees the real host.

## Flags

Most runs need no flags. `doctor` probes the backend this install shipped
with; use `--arch` when you plan to run non-native guests.

| Flag | Description |
|---|---|
| `--arch <arch>` | Guest architecture (`amd64`, `arm64`) |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--state-dir <dir>` | State directory the pasta start probe runs against (default `~/.microagent/`) |
| `--json` | Global flag before `doctor`; print structured JSON output |

See [global flags](index.md#global-flags) for `--output`/`--json`/`--supervisor`.

## Exit status

The verdict is three-way, and the exit code follows it:

- `ok` (exit `0`) — everything this backend advertises works on this host.
- `degraded` (exit `0`) — workspaces boot and run today, but a declared
  capability is not ready or a probe reported an issue. Whatever needs the
  missing piece fails closed instead of running without it — a request that
  needs an unavailable capability is refused rather than silently downgraded.
- `failed` (exit `1`) — the core boot path itself is broken; no run can work.

The printed page includes the full check detail in every case, and the same
verdict is in the structured output as `verdict`.

## Related

- [Backends](../concepts/backends.md) - what each backend requires
- [`host`](host.md) - the same data as a capability report
- [`kernel install`](kernel.md) - install the default kernel
