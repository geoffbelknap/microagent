---
title: Windows Hyper-V supervisor
description: Run Linux guests on Windows through HCS - no WSL, no QEMU.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-20_

If you want Linux guests on a Windows host - without WSL and without QEMU -
this page documents the `windows-hyperv` backend. It talks to
Windows Host Compute Service (HCS) through `vmcompute.dll` and prepares
Hyper-V utility VM-style compute systems from microagent runtime requests.

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

## Egress mediation

Egress mediation is supported for the `user` and `nat` network modes on
Windows Hyper-V.

### How it works

When a workspace starts in `user` or `nat` mode, the supervisor mints a
fresh per-workspace ECDSA P-256 CA and starts a host-side mediator process.
The workspace is placed on a no-uplink HNS network — its only path off the
host is the mediator — so traffic cannot reach the internet except through
that path.

Inside the guest, outbound TCP connections are transparently redirected to a
Hyper-V socket listener that connects to the host mediator. The host mediator
applies the egress policy (allow / passthrough / strict), performs per-SNI TLS
interception, resolves DNS, and audits every connection. Non-TCP and non-UDP
traffic (ICMP and the like) is dropped fail-closed. IPv6 egress is dropped
fail-closed while mediation ships v4-only.

The per-workspace CA's public certificate is delivered to the guest over a
Hyper-V socket channel at boot and installed into the guest's trust store, so
tools inside the guest trust the per-SNI leaf certificates the mediator signs.
The CA private key never leaves the host.

### Topology and enforcement

Policy enforcement is host-side: the mediator and the no-uplink topology
together ensure a workspace cannot reach the internet except through the
mediation path. A compromised guest can disrupt its own connectivity (the
capture runs inside the guest, so it could break the redirect), but it cannot
bypass the mediator — the host provides the only network path, and the mediator
is that path. At worst the workspace loses egress (fail-closed).

### Comparison with Linux/Firecracker

On the Linux/Firecracker backend, egress capture is enforced entirely host-side
via netfilter TPROXY rules that redirect guest traffic before it leaves the
hypervisor host network namespace; the guest plays no part in the capture. On
Windows Hyper-V the policy enforcement is likewise host-side (the mediator plus
the no-uplink topology), but the transparent redirect runs inside the guest via
nftables redirect rules. A compromised guest can therefore break its own egress
but cannot escape mediation.

### Mediation modes

The same three egress modes apply on Windows Hyper-V:

| Mode | Effect |
|---|---|
| `mediated` | All TCP egress is captured, TLS is intercepted per-SNI, DNS is resolved and audited. Nothing is blocked. (Default) |
| `strict` | Same capture, but only allowlisted destinations are permitted. Non-allowlisted DNS queries are answered REFUSED before any connection is attempted. |
| `off` | No capture. The workspace's HNS NAT endpoint has a standard uplink. |

See [Egress mediation](/concepts/egress-mediation/) for the full policy,
allowlist, passthrough, credential-swap, and audit-log documentation. View the
audit log with [`microagent egress`](/cli/egress/).

### `bridged` remains unmediated

`bridged` mode on Windows Hyper-V attaches the guest directly to the named HNS
network or Hyper-V switch you declare. That L2 presence bypasses the no-uplink
topology and the host mediator, so `bridged` workspaces are not egress-mediated.
`bridged` requires `--unsupported` and is outside the egress-mediation security
model, exactly as documented in [Networking](/concepts/networking/).

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

- Egress mediation is supported for `user` and `nat` (see
  [Egress mediation](#egress-mediation) above). `bridged` mode is not
  egress-mediated and requires `--unsupported`. `isolated` and named networks
  have no external egress and are unaffected.
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
