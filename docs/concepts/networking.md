---
title: Networking
description: Declarative workspace network intent.
---

Every workspace declares its network intent. Four modes:

| Mode | What it does |
|---|---|
| `user` | Default. Unprivileged outbound IPv4, plus declared TCP `--publish` forwards. |
| `nat` | Outbound IPv4 via backend NAT, plus declared TCP `--publish` forwards. |
| `isolated` | No guest network device. The body has no network access at all. |
| `bridged` | Workspace gets its own L2 presence on a host bridge. |

The implementation under each mode varies by backend:

| Backend | What works today |
|---|---|
| Apple VF | `user` and `nat` both map to `VZNATNetworkDeviceAttachment` — runs in user space inside the framework, no privileges required. `isolated` and TCP `--publish` work. `bridged` is implemented but gated by Apple's restricted `com.apple.vm.networking` entitlement, which open-source builds can't self-sign. |
| Firecracker | `user` runs Firecracker inside a `pasta` user namespace with a namespace-local TAP. `nat` creates a host-side TAP and installs nftables MASQUERADE rules. `bridged` attaches a transient TAP to an existing host Linux bridge. `isolated` and TCP `--publish` work. |
| Windows Hyper-V | `user` and `nat` use the managed `microagent-nat` HNS NAT network. `isolated` starts without an external network adapter. TCP `--publish` works through Hyper-V socket bridging. `bridged` attaches to the named HNS network or Hyper-V switch. |

Apple VF NAT is backend-managed by macOS. Microagent attaches `VZNATNetworkDeviceAttachment` and asks the kernel to do DHCP via `ip=dhcp`; it does not create a TAP, configure `pf`, or allocate a subnet of its own. Guest init writes `/etc/resolv.conf` from the kernel's DHCP nameserver data — so NAT works without an image-local DHCP client. Virtualization.framework's NAT API doesn't expose deterministic guest IP, gateway, or DNS, so Apple VF status reports the requested mode and declared publishes but not a runtime lease.

Windows Hyper-V NAT is backend-managed through HNS/HCN. Microagent creates or
reuses a `microagent-nat` network, attaches an HNS endpoint to the HCS compute
system, and records runtime network IDs and address details when Windows
returns them.

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

Under the hood, the guest init listens on a vsock port matching the host port; the backend supervisor runs the host-side TCP listener and bridges connections to that vsock port. You don't have to configure either side — declaring the forward wires it up.

Isolated workspaces reject port forwards before the request leaves the CLI: there's no guest network for them to reach.

## User mode on Firecracker

`user` is the default Linux mode. The workspace runs inside an unprivileged user namespace, with `pasta` bridging that namespace's network out to the host using ordinary user-space socket calls. No host `setcap`, no `ip_forward=1`, no bridge configuration — `pasta` does the equivalent of NAT entirely in user space.

Host requirements:

- `pasta` installed (`apt install passt` on Debian/Ubuntu, `dnf install passt` on Fedora). Homebrew installs it automatically.
- Unprivileged user namespaces enabled (`sysctl user.max_user_namespaces` returns a non-zero value).
- `/dev/net/tun` readable by the user.

`microagent doctor` checks all three and tells you which is missing.

## NAT on Firecracker

`nat` is the kernel-speed alternative to `user`. The supervisor creates a host-side TAP, assigns a `10.43.x.0/29` subnet, installs nftables MASQUERADE rules, and attaches the TAP as the guest's `eth0`. Guest-init configures the static IP, default route, and DNS resolvers from kernel command-line parameters. Outbound TCP and DNS work without a host bridge. Inbound stays closed unless you declare a `--publish` forward.

Host requirements:

- Linux kernel with nftables support (any 4.4+ kernel).
- `net.ipv4.ip_forward=1`. The supervisor doesn't toggle this for you — it's a host-wide policy decision.
- `CAP_NET_ADMIN` available to the supervisor process and inheritable by Firecracker. Running as root works. For a non-root flow, grant the supervisor `cap_net_admin,cap_setpcap+ep`; the supervisor uses `CAP_SETPCAP` to add `CAP_NET_ADMIN` to its inheritable set before it launches Firecracker.

If any of those is missing, `nat` fails closed before the VM boots. Transient TAPs and per-workspace nftables rules are cleaned up on `quarantine`, `stop`, `kill`, and `delete`.

Pick `nat` over `user` when you need the throughput — the user-mode stack costs ~10–30% on bandwidth, irrelevant for LLM API calls but noticeable for high-volume traffic.

## Bridged on Apple VF

Declare the host interface identifier or its localized display name:

```yaml
network:
  mode: bridged
  interface: en0
```

The supervisor needs the `com.apple.vm.networking` entitlement. Local ad-hoc builds fail closed during `check` with an error that names the Apple restriction — you'll see it before any VM tries to start.

## Bridged on Firecracker

`interface` must name an existing Linux bridge:

```yaml
network:
  mode: bridged
  interface: br0
```

The supervisor creates a transient TAP, attaches it to the bridge via Linux netlink, writes the Firecracker network device config, and tears the TAP down on `quarantine`/`stop`/`kill`/`delete`. Missing privileges, non-bridge interfaces, and TAP setup failures all fail closed.

Same `CAP_NET_ADMIN` requirement as `nat` — run as root, or use a supervisor binary with `cap_net_admin,cap_setpcap+ep` so Firecracker can inherit `CAP_NET_ADMIN`.

## Bridged on Windows Hyper-V

Declare an existing HNS network or Hyper-V switch name:

```yaml
network:
  mode: bridged
  interface: External Switch
```

The Windows backend fails closed if the named network cannot be found or if
the current user cannot create HNS endpoints for HCS compute systems.

## Mediation channel

Mediation is a separate guest-to-host vsock contract for the body's calls into the host control plane — distinct from ordinary networking. Declare it with:

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

The network record appears in JSON output from `create`, `start`, `status`, and `ps`. `microagent --json network <name>` also shows the latest runtime network assignment, including Firecracker NAT IP, subnet, gateway, DNS, and route when present. Apple VF reports the declared mode and any port forwards but can't expose the macOS-managed NAT lease — Virtualization.framework doesn't surface it. Windows Hyper-V reports HNS network and endpoint identity plus address details returned by HCN. Low-level wiring such as TAP names, HNS endpoint IDs, and Firecracker config paths stays behind the supervisor protocol. Malformed port forwards fail closed before any request is sent.
