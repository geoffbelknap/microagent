---
title: Windows Hyper-V supervisor
description: Run Linux guests on Windows through HCS - no WSL, no QEMU.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-21_

If you want Linux guests on a Windows host - without WSL and without QEMU -
this page documents the `windows-hyperv` backend. It talks to
Windows Host Compute Service (HCS) through `vmcompute.dll` and prepares
Hyper-V utility VM-style compute systems from microagent runtime requests.

Status: `windows-hyperv` is experimental. Behavior and coverage may change.
See [Platform support](/concepts/platform-support/) for the support policy.

For the shared command list and response shape, see
[Supervisor protocol](/protocol/). This page covers the Windows host behavior
and current limitations.

## Host requirements

`windows-hyperv` requires:

- Windows with Host Compute Service available
- Hyper-V / Windows Hypervisor Platform support enabled
- a user token that can access HCS, typically Administrator or membership in
  the Hyper-V Administrators group
- a Linux kernel artifact for `windows-hyperv/<arch>`
- a `microagent-guestinit-<arch>` guest init binary
- a VHD root disk at the workspace rootfs path

Use:

```powershell
microagent doctor --backend windows-hyperv
```

The doctor check reports HCS availability, virtualization support, HCS access
errors, HCN/HNS networking availability, Hyper-V socket availability, kernel
support, guest-init availability, and console capability.

## Storage

Windows Hyper-V consumes a VHD root disk because HCS VM configuration is
VHD-oriented. Workspace root disks live under:

```text
<state-dir>/workspaces/<runtimeID>/rootfs.vhd
```

The source contents still come from microagent's OCI/rootfs flow. The Windows
rootfs builder converts those contents into a fixed VHD with an ext4 payload.

Bundled data disks are also built as fixed VHD ext4 images and attached to the
same HCS SCSI controller after the root disk. The guest sees the root disk as
`/dev/sda`, then configured data disks as `/dev/sdb`, `/dev/sdc`, and so on.
Disk `mode` maps to the HCS attachment's `ReadOnly` flag and the guestinit
mount mode.

## Lifecycle

The current lifecycle surface is:

| Command | Status |
|---|---|
| `host` | supported |
| `check` | supported |
| `prepare` | supported |
| `run` | supported |
| `inspect` | supported |
| `start` | supported |
| `halt` | supported |
| `quarantine` | supported |
| `stop` | supported |
| `kill` | supported |
| `delete` | supported |
| `console` | unsupported (non-goal; use `connect`) |

Unsupported commands fail closed with structured `ok: false` responses.

`prepare` writes the backend-neutral prepared state files for service-style
`create` flows without creating an HCS compute system. `run` creates an HCS
compute system, waits for guest result delivery, records backend-neutral
runtime state, and returns a stopped event with `result` when the guest exits
successfully. `start` creates a detached HCS compute system and records enough
HCS identity in `runtime.json` for later `inspect`, `halt`, `quarantine`,
`stop`, `kill`, and `delete` (and for CLI-level `connect` over Hyper-V sockets,
which is not a supervisor protocol command).

## Networking

Windows Hyper-V uses HNS/HCN networking for guest NIC attachment:

| Mode | Behavior |
|---|---|
| `user` | uses the managed `microagent-nat` HNS NAT network |
| `nat` | uses the managed `microagent-nat` HNS NAT network |
| `isolated` | starts without an external network adapter |
| `bridged` | attaches to the named HNS network or Hyper-V switch from `network.interface`; the guest takes the endpoint's static address when the network allocates one, otherwise it DHCPs |

The managed NAT network uses `192.168.127.0/24` with gateway
`192.168.127.1`. Runtime network details, including the HNS network and
endpoint IDs, are recorded in `runtime.json`.

Published TCP ports from `network.portForwards` bind host TCP listeners and
bridge accepted connections to the guest through Hyper-V sockets using the
configured `hostPort` as the Hyper-V socket service. The guest-side init then
proxies that stream to the configured `guestPort`. The listener helper is torn
down during `quarantine`, `halt`, `stop`, `kill`, and `delete`.

Bridged mode fails closed unless `network.interface` names an existing HNS
network or Hyper-V switch. If the named network statically allocates an
endpoint address at attach time (a managed NAT-style network), the guest is
configured with that static address; if it does not (an external vSwitch, or
the built-in ICS `Default Switch`, which serve addresses over DHCP), the guest
DHCPs on its NIC instead. Endpoint cleanup runs when foreground `run` completes
and during `quarantine`, `halt`, `stop`, `kill`, and `delete`.

## Structured exec

`microagent exec` (buffered and `--stream`) works against running
`windows-hyperv` workspaces. The supervisor binds a host TCP listener on
`127.0.0.1:<execPort>` and bridges each accepted connection to the guest's
structured exec service over Hyper-V sockets - the same bridge mechanic as
published TCP ports. The exec port is recorded in `runtime.json`, so the
workspace layer dials it exactly like the other backends.

If the configured exec port cannot be bound on the host - the default exec
port range overlaps the Windows dynamic TCP range, so an ephemeral outbound
connection can transiently hold it - the supervisor moves the host bind to a
free port and keeps the original port as the guest's Hyper-V socket service,
so the bridge and the guest's own listener still agree.

Detached `start` fails closed when the listener helper's exec bridge does not
accept on the host, instead of reporting a running workspace whose exec
channel is silently dead. The helper's lifetime is bounded by the workspace:
it is torn down during `quarantine`, `halt`, `stop`, `kill`, and `delete`, and
it exits when the guest stops on its own.

## State

The supervisor writes backend runtime files under:

```text
<state-dir>/<runtimeID>/
```

Important files include:

| File | Purpose |
|---|---|
| `event.json` | latest lifecycle event |
| `events.json` | append-only lifecycle history |
| `runtime.json` | latest lifecycle state and HCS compute system ID |
| `serial.in` | console input compatibility marker for running workspaces |
| `serial.log` | guest serial output captured from the HCS COM1 named pipe |
| `result.json` | structured guest result when delivered |
| `hvsock-listener.log` | detached Hyper-V socket listener helper log |

`inspect` returns the latest event and readiness computed from the channels
themselves: `guestReady` reflects the recorded runtime state, `shellReady`
reflects a Hyper-V socket dial of the guest shell service, and `execReady`
reflects a structured exec round-trip through the host exec bridge. If
`result.json` exists, `inspect` also returns the backend-neutral `result`
object and marks `readiness.resultReady.ready` true.

## Transport mechanics

- No WSL dependency is used or required.
- QEMU/WHPX is not used.
- `microagent connect` and `connect --send` use Hyper-V sockets.
- `microagent exec` (buffered and `--stream`) uses the host exec bridge over
  Hyper-V sockets.
- Mediation and guest-to-host TCP listener targets use Hyper-V socket listener
  helpers.
- Foreground `run` supports the configured result listener by mapping the guest
  AF_VSOCK result port to a Hyper-V socket service and writing the received
  payload to `result.json`.
- Result runs configure COM1 as an HCS named pipe and append guest serial output
  to `serial.log`.

## Current limitations

- `bridged` networking requires `network.interface` to name an existing HNS
  network or Hyper-V switch and fails closed when it is missing. The DHCP path
  (guest addressed by the bridged network) is live-verified against the
  built-in ICS `Default Switch`; bridging to an external vSwitch on the
  physical LAN follows the same path but is exercised manually, since hosted CI
  runners have no external switch. `user`, `nat`, and `isolated` are all
  live-verified.
- HNS `user`/`nat`/`bridged` segments need an elevated host to provision or
  attach HNS networks.
- `survive-reboot` registers a Scheduled Task when run elevated; an unelevated
  host surfaces the manual `schtasks` command to register instead.
- `pause`/`resume` freeze and thaw a running workspace's vCPUs in place via
  `HcsPauseComputeSystem`/`HcsResumeComputeSystem` (memory, disk, the compute
  system registration, and the runtime listener helper are all preserved).
- `snapshot`/save-state is **not supported and not planned**: HCS-direct
  (`LinuxKernelDirect`) compute systems have no guest-memory save-state. The
  HCS save call captures only device state, and the Hyper-V mechanisms that do
  save memory (`Save-VM`, checkpoints) belong to VMMS, which this backend
  deliberately does not use. Snapshot commands fail closed; use `commit` (a
  distributable image) or `clone` (a disk copy) instead.
- Named networks are supported: `network create`/`ls`/`delete` plus
  `--network named --network-name <n>` back onto a private HNS network with
  static IPAM (members share a subnet and address each other).
- Direct supervisor `console` is a deliberate non-goal on every backend; use
  `microagent connect`, which is the interactive contract.

The backend fails closed when a host prerequisite or unsupported feature is
missing.
