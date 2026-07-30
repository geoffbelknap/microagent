---
title: Networking
description: Choose `user` or `isolated`, publish ports, and read network status.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-30_

This page defines the workspace network modes and what `--publish` does. For a
walkthrough, see [Networking](/guides/networking/). The guest-to-host
mediation channel has its own guide:
[Build agents on the mediation channel](/guides/agents-and-mediation/).

Every workspace declares one network mode. `user` is the default. `isolated`
turns the guest network device off.

| Mode | What it does |
|---|---|
| `user` | Default. Unprivileged outbound IPv4, plus declared TCP `--publish` forwards. |
| `isolated` | No guest network device. The guest has no network access at all. |

Network mode controls the guest's network device. What the guest may send over
that device is handled by [egress mediation](/concepts/egress-mediation/):
allowlists, passthrough hosts, credential swap, and audit events. The default
`broker` mode observes, allows, and denies traffic without forging any
certificate; TLS interception happens only in `mitm` mode.

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

A `--publish` flag and a `network.forwards[]` entry in the spec mean the same
thing. The CLI form is shorthand for one forward object.

You don't have to configure routing or a host bridge. Declaring the forward
wires the host listener to the guest port.

When the mapping binds a concrete IPv4 host address, forwarded connections
preserve that address as the guest application's local address. This lets
protocols that advertise a callback or media endpoint report the address their
clients actually reached. A wildcard bind such as `0.0.0.0` cannot preserve one
address because connections may arrive through different interfaces; use a
concrete address or configure the application's external address explicitly.

Isolated workspaces reject port forwards before the request leaves the CLI:
there is no guest network for them to reach.

## Inbound networking

Use `--publish` for host-to-guest TCP services:

```bash
microagent create web --network user --publish 127.0.0.1:8080:80/tcp
```

This is the portable way to accept inbound connections: the host listens on
the declared address and port, then forwards to the requested guest TCP port.
It works
the same way for HTTP services, SSH-like services, and local test servers.

Do not depend on direct host routing to the guest IP. `user` mode is a NAT
mode. A stable guest IP is useful for deterministic guest-side config, tests,
and software that binds to a non-loopback address. But it is not the portable
way to expose a service to the host. Published ports are.

## Static address

You can declare a static guest IPv4 configuration when a test or workload needs
a stable guest-side address:

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

Use `--publish` for host-to-guest access. A stable guest IP is useful for
guest-side configuration, but published ports are the portable way to expose a
service to the host.

## Mediation channel

The **mediation channel** is a guest-to-host **vsock** path for calls into your
host control plane. It is separate from ordinary networking and is required by
default. Declaration syntax, the host listener pattern, and failure behavior
all live in [build agents on the mediation channel](/guides/agents-and-mediation/).

> **Don't confuse the two "mediations."** The mediation channel is a vsock side
> channel into your control plane. Egress mediation controls the guest's
> ordinary TCP/UDP/DNS traffic.

## What's visible

The network record appears in JSON output from `create`, `start`, `status`,
and `list`. `microagent --json network <name>` shows the declared mode, static
network fields when present, published ports, and runtime address details when
the host reports them. Malformed port forwards fail before microagent starts
the workspace.
