---
title: Networking
description: Declarative workspace network intent.
---

Every workspace records its network intent in the manifest. The CLI accepts four modes:

| Mode | What it does |
|---|---|
| `user` | Default on Linux. Unprivileged outbound IPv4 through pasta user-mode networking, plus declared TCP `--publish` forwards. |
| `nat` | Outbound IPv4 via backend NAT, plus declared TCP `--publish` forwards. |
| `isolated` | No guest network device. The body has no network access at all. |
| `bridged` | Attach to a host bridge so the workspace gets its own L2 presence. Backend support varies. |

Backend support is narrower than the enum:

| Backend | What works today |
|---|---|
| Apple VF | `nat`, `isolated`, and TCP `--publish`. `bridged` is implemented but blocked in open-source builds by Apple's restricted `com.apple.vm.networking` entitlement. |
| Firecracker | `user` through pasta plus a namespace-local TAP, `nat` through a transient TAP and nftables MASQUERADE, live TCP `--publish`, `isolated`, and `bridged` through a host Linux bridge. |

Apple gates native bridged networking behind `com.apple.vm.networking`. Open-source builds can't self-sign that entitlement, and `sudo` doesn't bypass the check. If you need bridged on macOS, you sign with the entitlement; otherwise, use `nat`.

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

## User Networking on Firecracker

Firecracker `user` mode is the default Linux networking mode. The supervisor
re-execs itself under `pasta`, which creates an unprivileged user and network
namespace. Inside that namespace the supervisor creates the Firecracker TAP,
configures namespace-local nftables forwarding, and starts Firecracker. Pasta
bridges the namespace to the host network with ordinary user sockets.

Host requirements:

- `pasta` installed (`apt install passt` on Debian/Ubuntu)
- unprivileged user namespaces enabled
- `/dev/net/tun` available to the user

No `setcap`, host `ip_forward`, host bridge, or host firewall edits are needed
for `user` mode.

## NAT on Firecracker

Firecracker `nat` mode creates a host-side TAP device, assigns a private
`10.43.x.0/29` subnet, configures nftables MASQUERADE, and attaches the TAP as
the guest's `eth0`. Guest-init configures a static IPv4 address, installs the
default route through the TAP gateway, and writes DNS resolvers. Outbound TCP
and DNS work without a host bridge. Inbound remains closed unless you declare
specific TCP forwards with `--publish`.

Host requirements:

- Linux kernel 4.4 or newer with nftables support
- `net.ipv4.ip_forward=1`
- permission to create TAP devices and edit nftables rules, typically root or
  `setcap cap_net_admin+eip <supervisor>` on the Firecracker supervisor binary

The supervisor does not enable `ip_forward` for you because it is host-wide
policy. If a requirement is missing, `nat` fails closed before booting the VM.
Transient TAP devices and per-workspace nftables rules are removed on
`quarantine`, `stop`, `kill`, and `delete`.

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

The supervisor creates a transient TAP device, attaches it to the bridge, writes the Firecracker network device config, and removes the TAP when the workspace is quarantined, stopped, killed, or deleted. Missing privileges, non-bridge interfaces, and TAP setup failures all fail closed.
The supervisor uses Linux netlink directly, so bridged mode does not require
the `ip` command at runtime.

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

For the architecture and a worked pattern, see [Wire up the mediation channel](../recipes/mediation-channel.md).

## What's visible

The network record appears in JSON output from `create`, `start`, `status`, and
`ps`. `microagent --json network <name>` also shows the latest runtime network
assignment, including Firecracker NAT IP, subnet, gateway, DNS, and route when
present. Low-level wiring such as TAP names and Firecracker config paths stays
behind the supervisor protocol. Malformed port forwards fail closed before any
request is sent.
