---
title: Networking
description: Declarative workspace network intent.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-03_

Every workspace declares its network intent. Five modes:

| Mode | What it does |
|---|---|
| `user` | Default. Unprivileged outbound IPv4, plus declared TCP `--publish` forwards. |
| `nat` | Outbound IPv4 via backend NAT, plus declared TCP `--publish` forwards. |
| `isolated` | No guest network device. The guest has no network access at all. |
| `bridged` | Workspace gets its own L2 presence on an existing host bridge. |
| `named` | Workspace joins a [user-defined named network](#named-networks): a stable IP from the network's subnet, a shared managed bridge so members reach each other, and `/etc/hosts` name resolution. Currently implemented by Firecracker on Linux. |

The implementation under each mode varies by backend. Quick matrix across all
three backends:

| Backend | What works today |
|---|---|
| Apple VF | `user` and `nat` both map to `VZNATNetworkDeviceAttachment` - runs in user space inside the framework, no privileges required. `isolated`, static NAT config, and TCP `--publish` work. `bridged` is implemented but gated by Apple's restricted `com.apple.vm.networking` entitlement, which open-source builds can't self-sign. |
| Firecracker | `user` runs Firecracker inside a `pasta` user namespace with a namespace-local TAP. `nat` creates a host-side TAP and installs nftables MASQUERADE rules. `bridged` attaches a transient TAP to an existing host Linux bridge. `isolated` and TCP `--publish` work. |
| Windows Hyper-V | `user` and `nat` use the managed `microagent-nat` HNS NAT network. `isolated` starts without an external network adapter. TCP `--publish` works through Hyper-V socket bridging. `bridged` attaches to the named HNS network or Hyper-V switch. |

Apple VF NAT is backend-managed by macOS. Microagent attaches `VZNATNetworkDeviceAttachment`; it does not create a TAP, configure `pf`, or allocate a subnet of its own. By default it asks the kernel to do DHCP via `ip=dhcp`, and guest init writes `/etc/resolv.conf` from the kernel's DHCP nameserver data, so NAT works without an image-local DHCP client. When a spec declares `network.ip`, `network.gateway`, and optional `network.dns`, Apple VF passes those values to guest init for static IPv4 setup. Use the macOS NAT subnet, normally `192.168.64.0/24` with gateway `192.168.64.1`; Virtualization.framework still owns the attachment and does not expose an independently allocated runtime lease.

Windows Hyper-V NAT is backend-managed through HNS/HCN. Microagent creates or
reuses a `microagent-nat` network, attaches an HNS endpoint to the HCS compute
system, and records runtime network IDs and address details when Windows
returns them.

## Backend parity

The portable contract is the network intent: outbound access in `user` and
`nat`, no guest network in `isolated`, declared TCP inbound through
`--publish`, and static guest IP/DNS config where the backend can safely apply
it.

The backend mechanics are intentionally different. The table below is a
Firecracker-vs-Apple VF deep dive on the two production backends; experimental
Windows Hyper-V is covered in the quick matrix above and in
[Backends](/concepts/backends/).

| Capability | Firecracker on Linux | Apple VF on macOS |
|---|---|---|
| `user` | `pasta` user-mode networking in an unprivileged namespace. | Native `VZNATNetworkDeviceAttachment`; no sudo, TAP, `pf`, or bridge setup. |
| `nat` | Microagent creates a TAP, assigns a `10.43.x.0/29` subnet, and installs nftables NAT rules. | macOS owns the native NAT attachment. Microagent can configure a static guest address inside Apple's NAT subnet, normally `192.168.64.0/24`. |
| Runtime lease reporting | Reports assigned runtime IP, subnet, gateway, DNS, and routes because microagent owns the allocation. | Reports declared static config and published ports. DHCP lease details stay macOS-managed because Virtualization.framework does not expose them. |
| Bridged | Works with an existing Linux bridge and host network privileges. | Implemented in the supervisor, but normal open-source builds are blocked by Apple's restricted `com.apple.vm.networking` entitlement. |

For most workloads, use `user` unless you need static guest config or Linux TAP
NAT throughput. On macOS, `user` and `nat` both use Apple's native NAT
attachment; `nat` is the mode to choose when you want to declare a stable
guest IP/gateway/DNS inside that native NAT network.

## Declaring the mode

```bash
microagent create research --network user
```

Or in the spec:

```yaml
network:
  mode: user
  forwards:
    - host: 127.0.0.1
      hostPort: 8080
      guestPort: 80
      protocol: tcp
```

## Port forwards (`--publish`)

Repeat `--publish` for each TCP forward you need:

```bash
microagent create research --publish 127.0.0.1:8080:80/tcp
```

A `--publish` flag and a `network.forwards[]` entry in the spec are the same
thing - the CLI form is just shorthand for one forward object.

Under the hood, the guest init listens on a vsock port matching the host port; the backend supervisor runs the host-side TCP listener and bridges connections to that vsock port. You don't have to configure either side - declaring the forward wires it up.

Isolated workspaces reject port forwards before the request leaves the CLI: there's no guest network for them to reach.

## Inbound networking

Use `--publish` for host-to-guest TCP services:

```bash
microagent create web --network user --publish 127.0.0.1:8080:80/tcp
```

That is the backend-neutral inbound contract. The host listens on the declared
address and port, the supervisor bridges the connection over the backend's
transport, and guest init forwards it to the requested guest TCP port. It works
the same way for HTTP services, SSH-like services, and local test servers.

Do not depend on direct host routing to the guest IP unless you are deliberately
using a backend-specific bridged setup. Firecracker `nat` and Apple VF `nat`
are NAT modes; a stable guest IP is useful for deterministic guest-side config,
tests, and software that binds to a non-loopback address, but it is not the
portable way to expose a service to the host. Published ports are.

## User mode on Firecracker

`user` is the default Linux mode. The workspace runs inside an unprivileged user namespace, with `pasta` bridging that namespace's network out to the host using ordinary user-space socket calls. No host `setcap`, no `ip_forward=1`, no bridge configuration - `pasta` does the equivalent of NAT entirely in user space.

Host requirements:

- `pasta` installed (`apt install passt` on Debian/Ubuntu, `dnf install passt` on Fedora). Homebrew installs it automatically.
- Unprivileged user namespaces enabled (`sysctl user.max_user_namespaces` returns a non-zero value).
- `/dev/net/tun` readable by the user.

`microagent doctor` checks all three and tells you which is missing.

## NAT on Firecracker

`nat` is the kernel-speed alternative to `user`. The supervisor creates a host-side TAP, assigns a `10.43.x.0/29` subnet, installs nftables MASQUERADE rules, and attaches the TAP as the guest's `eth0`. Guest-init configures the static IP, default route, and DNS resolvers from kernel command-line parameters. Outbound TCP and DNS work without a host bridge. Inbound stays closed unless you declare a `--publish` forward.

Host requirements:

- Linux kernel with nftables support (any 4.4+ kernel).
- `net.ipv4.ip_forward=1`. The supervisor doesn't toggle this for you - it's a host-wide policy decision.
- `CAP_NET_ADMIN` available to the supervisor process and inheritable by Firecracker. Running as root works. For a non-root flow, grant the supervisor `cap_net_admin,cap_setpcap+ep`; the supervisor uses `CAP_SETPCAP` to add `CAP_NET_ADMIN` to its inheritable set before it launches Firecracker.

If any of those is missing, `nat` fails closed before the VM boots. Transient TAPs and per-workspace nftables rules are cleaned up on `quarantine`, `stop`, `kill`, and `delete`.

Pick `nat` over `user` when you need the throughput. The user-mode stack costs
about 10-30% on bandwidth. That does not matter much for LLM API calls, but it
shows up on high-volume traffic.

## Static NAT on Apple VF

Apple VF can apply static guest IPv4 inside the native macOS NAT subnet:

```yaml
network:
  mode: nat
  ip: 192.168.64.2/24
  subnet: 192.168.64.0/24
  gateway: 192.168.64.1
  dns:
    - 1.1.1.1
    - 8.8.8.8
  routes:
    - 0.0.0.0/0 via 192.168.64.1
```

This is not a TAP-style backend allocation. Microagent passes the declared
address, gateway, and DNS values to guest init; macOS still provides the NAT
attachment. Use static NAT when a test or workload needs a stable guest address
inside the Apple VF NAT network.

## Bridged on Apple VF

Declare the host interface identifier or its localized display name:

```yaml
network:
  mode: bridged
  interface: en0
```

The supervisor needs the `com.apple.vm.networking` entitlement. Local ad-hoc builds fail closed during `check` with an error that names the Apple restriction - you'll see it before any VM tries to start.

## Bridged on Firecracker

`interface` must name an existing Linux bridge:

```yaml
network:
  mode: bridged
  interface: br0
```

The supervisor creates a transient TAP, attaches it to the bridge via Linux netlink, writes the Firecracker network device config, and tears the TAP down on `quarantine`/`stop`/`kill`/`delete`. Missing privileges, non-bridge interfaces, and TAP setup failures all fail closed.

Same `CAP_NET_ADMIN` requirement as `nat` - run as root, or use a supervisor binary with `cap_net_admin,cap_setpcap+ep` so Firecracker can inherit `CAP_NET_ADMIN`.

## Bridged on Windows Hyper-V

Declare an existing HNS network or Hyper-V switch name:

```yaml
network:
  mode: bridged
  interface: External Switch
```

The Windows backend fails closed if the named network cannot be found or if
the current user cannot create HNS endpoints for HCS compute systems.

## Named networks

`bridged` mode attaches to a bridge *you* already created. A **named network**
is microagent's own managed equivalent: declare it once, and any number of
workspaces join it by name and become peers on a shared subnet. It is the
in-boundary analog of a Docker user-defined network. Workspace attachment is
currently implemented by the Firecracker/Linux backend; Apple VF does not
currently implement `network.mode=named`.

Create the network (a VM-independent registry record, no host devices yet):

```bash
microagent network create devnet              # auto-allocated /24 from 10.44.0.0/16
microagent network create devnet --subnet 10.44.50.0/24
```

Join workspaces with `--network-name` on `create`/`run`:

```bash
microagent create web --image docker.io/library/python:3.12 --network-name devnet
microagent create db  --image docker.io/library/postgres:16 --network-name devnet
microagent exec web -- ping db                # reach a peer by name
```

What joining does, realized by the Firecracker supervisor at start:

- **Stable address.** Each member is allocated the lowest free host in the
  network's subnet (the gateway is `.1`), persisted in the registry so the
  address survives stop/start. Deleting a workspace frees its address.
- **Shared bridge.** A managed Linux bridge (`mbr<hash>`) is created on demand
  with the gateway address; each member's TAP is enslaved to it, so members are
  on one L2 segment and reach each other directly. The bridge is reaped once the
  last member stops — no orphan devices.
- **Name resolution.** `/etc/hosts` is injected at boot from the current member
  set via the kernel-cmdline → guest-init seam (`microagent_net_hosts`,
  parallel to DNS). Outbound egress goes through the gateway with NAT, exactly
  like `nat` mode.

`/etc/hosts` is a **boot-time snapshot**: a member resolves peers that joined
*before* it booted. Reachability by IP is always order-independent (it's L2);
to refresh an earlier member's name table after newcomers join, restart it.
Live cross-member name updates are not currently implemented.

Host requirements match `nat`: `net.ipv4.ip_forward=1` and `CAP_NET_ADMIN` in
the supervisor (run as root, or grant `cap_net_admin,cap_setpcap+ep`).
`network rm` fails closed while members exist; pass `--force` to override. See
[`network`](/cli/network/) for the full command surface and
[Connect two workspaces](/recipes/connected-workspaces/) for a worked example.

## Mediation channel

Mediation is a separate guest-to-host vsock contract for the guest's calls into the host control plane - distinct from ordinary networking. Declare it with:

```bash
microagent create research --mediation 2048=127.0.0.1:9900
```

By default the channel is required and fail-closed: if the host listener isn't reachable, the workspace refuses to start. The same shape goes in `microagent.yaml`:

```yaml
mediation:
  enabled: true
  required: true
  port: 2048
  target: 127.0.0.1:9900
  failClosed: true
```

Use `--mediation-optional` only for development paths where the workspace may boot without the host-side mediator.

For the architecture and a worked pattern, see [Wire up the mediation channel](/recipes/mediation-channel/).

## What's visible

The network record appears in JSON output from `create`, `start`, `status`, and `ps`. `microagent --json network <name>` also shows the latest runtime network assignment, including Firecracker NAT IP, subnet, gateway, DNS, and route when present. Apple VF reports the declared mode, static network fields, and any port forwards, but DHCP NAT details remain macOS-managed because Virtualization.framework doesn't surface the lease. Windows Hyper-V reports HNS network and endpoint identity plus address details returned by HCN. Low-level wiring such as TAP names, HNS endpoint IDs, and Firecracker config paths stays behind the supervisor protocol. Malformed port forwards fail closed before any request is sent.
