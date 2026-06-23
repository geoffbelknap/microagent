---
title: Networking
description: Choose a network mode and see what each one does under the hood on each backend.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-23_

This is the internals page for workspace networking: read it to choose a
network mode and to understand what each mode actually does on each backend.
For the task-shaped walkthroughs, see [Networking](/guides/networking/); the
guest-to-host mediation channel has its own guide at
[Build agents on the mediation channel](/guides/agents-and-mediation/).

Every workspace declares its network intent. `user` (the default) and
`isolated` are the two network modes. Transparent
[egress mediation](/concepts/egress-mediation/) is implemented by the
Firecracker backend's `user` path; Apple VF's native NAT attachments are not
transparently mediated.

| Mode | What it does |
|---|---|
| `user` | Default. Unprivileged outbound IPv4, plus declared TCP `--publish` forwards. Runs in a per-VM user namespace with no host privileges. Egress-mediated on Firecracker/Linux. On Apple VF/macOS it uses the native unmediated Virtualization.framework NAT attachment. |
| `isolated` | No guest network device. The guest has no network access at all. |

This page is about *which network device a workspace gets and how it is wired*.
What the workspace is allowed to send over that device - capture, allowlists,
TLS interception, and the audit trail - is a separate layer:
[egress mediation](/concepts/egress-mediation/), on by default. Two distinct
things share the word "mediation" here; see [Mediation channel](#mediation-channel)
below for the disambiguation.

The implementation under each mode varies by backend. Quick matrix across all
three backends:

| Backend | What works today |
|---|---|
| Apple VF | `user` and `isolated`, static address config inside the native NAT subnet, and TCP `--publish` work. `user` maps to the native `VZNATNetworkDeviceAttachment`. |
| Firecracker | `user` runs Firecracker inside a `pasta` user namespace with a namespace-local TAP. `isolated` and TCP `--publish` work. |
| Windows Hyper-V | Experimental. `user` uses the managed `microagent-nat` HNS NAT network. `isolated` starts without an external network adapter. TCP `--publish` works through Hyper-V socket bridging. |

Apple VF NAT is backend-managed by macOS: `user` maps to
`VZNATNetworkDeviceAttachment`, which runs in user space inside the framework
with no privileges required, so microagent does not create a TAP, configure
`pf`, or allocate a subnet of its own. By default it asks the kernel to do DHCP
via `ip=dhcp`, and guest init writes `/etc/resolv.conf` from the kernel's DHCP
nameserver data, so NAT works without an image-local DHCP client. When a spec
declares `network.ip`, `network.gateway`, and optional `network.dns`, Apple VF
passes those values to guest init for static IPv4 setup. Use the macOS NAT
subnet, normally `192.168.64.0/24` with gateway `192.168.64.1`;
Virtualization.framework still owns the attachment and does not expose an
independently allocated runtime lease.

Windows Hyper-V NAT is backend-managed through HNS/HCN: microagent creates or
reuses a `microagent-nat` network, attaches an HNS endpoint to the HCS compute
system, and records runtime network IDs and address details when Windows
returns them.

## Backend parity

The portable contract is the network intent: outbound access in `user`, no
guest network in `isolated`, declared TCP inbound through `--publish`, and
static guest IP/DNS config where the backend can safely apply it.

The backend mechanics are intentionally different. The table below is a
Firecracker-vs-Apple VF deep dive on those two backends; Windows Hyper-V is
covered in the quick matrix above and in [Backends](/concepts/backends/).

| Capability | Firecracker on Linux | Apple VF on macOS |
|---|---|---|
| `user` | `pasta` user-mode networking in an unprivileged namespace. | Native `VZNATNetworkDeviceAttachment`; no sudo, TAP, `pf`, or bridge setup. |
| Runtime lease reporting | Reports assigned runtime IP, subnet, gateway, DNS, and routes because microagent owns the per-VM namespace allocation. | Reports declared static config and published ports. DHCP lease details stay macOS-managed because Virtualization.framework does not expose them. |

For most workloads, use `user`. On macOS, `user` uses Apple's native NAT
attachment; declare `network.ip`/`gateway`/`dns` when you want a stable guest
address inside that native NAT network.

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

Do not depend on direct host routing to the guest IP. `user` mode is a NAT
mode; a stable guest IP is useful for deterministic guest-side config, tests,
and software that binds to a non-loopback address, but it is not the portable
way to expose a service to the host. Published ports are.

## User mode on Firecracker

`user` is the default Linux mode. The workspace runs inside an unprivileged per-VM user namespace, with `pasta` bridging that namespace's network out to the host using ordinary user-space socket calls. No host `setcap`, no `ip_forward=1`, no bridge configuration - `pasta` does the equivalent of NAT entirely in user space.

Host requirements:

- `pasta` installed (`apt install passt` on Debian/Ubuntu, `dnf install passt` on Fedora). Homebrew installs it automatically.
- Unprivileged user namespaces enabled (`sysctl user.max_user_namespaces` returns a non-zero value).
- `/dev/net/tun` readable by the user.

`microagent doctor` checks all three and tells you which is missing.

On hardened hosts that disable unprivileged user namespaces (`kernel.unprivileged_userns_clone=0`, `user.max_user_namespaces=0`, or an AppArmor userns restriction), `pasta` cannot create its namespace and the rootless `user` mode fails to start. Rather than surfacing the raw `pasta` error, the supervisor detects this case - either from those sysctl gates or from a namespace-creation failure signature in `pasta`'s stderr - and returns a guiding error that names the disabled gate, preserves the original `pasta` output, and points at the fix:

- Enable unprivileged user namespaces: `sudo sysctl -w kernel.unprivileged_userns_clone=1`.

If the host policy can't be changed, use `--network isolated` when the guest
does not need network access.

## Static address on Apple VF

Apple VF can apply static guest IPv4 inside the native macOS NAT subnet:

```yaml
network:
  mode: user
  ip: 192.168.64.2/24
  subnet: 192.168.64.0/24
  gateway: 192.168.64.1
  dns:
    - 1.1.1.1
    - 8.8.8.8
  routes:
    - 0.0.0.0/0 via 192.168.64.1
```

This is not a TAP-style backend allocation - microagent passes the declared
address, gateway, and DNS values to guest init; macOS still provides the NAT
attachment. Use a static address when a test or workload needs a stable guest
address inside the Apple VF NAT network.

## Mediation channel

The **mediation channel** is a guest-to-host **vsock** contract for the guest's calls into the host control plane - distinct from ordinary networking, and required and fail-closed by default. Declaration syntax, the host listener pattern, and the failure semantics all live in [build agents on the mediation channel](/guides/agents-and-mediation/).

> **Don't confuse the two "mediations."** The *mediation channel* (this section) is a vsock side channel into your control plane. *[Egress mediation](/concepts/egress-mediation/)* is the capture-and-control layer over the guest's ordinary network egress - the TCP/UDP/DNS it sends out of its network device, intercepted, allowlisted, and audited. Different mechanisms, different purposes.

## What's visible

The network record appears in JSON output from `create`, `start`, `status`, and `list`. `microagent --json network <name>` also shows the latest runtime network assignment, including the Firecracker per-VM NAT IP, subnet, gateway, DNS, and route when present. Apple VF reports the declared mode, static network fields, and any port forwards, but DHCP NAT details remain macOS-managed because Virtualization.framework doesn't surface the lease. Windows Hyper-V reports HNS network and endpoint identity plus address details returned by HCN. Low-level wiring such as TAP names, HNS endpoint IDs, and Firecracker config paths stays behind the supervisor protocol. Malformed port forwards fail closed before any request is sent.
