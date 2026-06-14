---
title: microagent network
description: Inspect workspace networking and manage named networks.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-14_

```text
microagent network <workspace> [--state-dir <dir>]   Inspect a workspace's network
microagent network create <name> [--subnet <cidr>]   Create a named network
microagent network list                                 List named networks
microagent network delete <name> [--force]                Remove a named network
```

With a workspace name, `network` reports the network mode, bridged host
interface, declared port forwards, DNS servers, routes, and IP information
recorded for that workspace. The top-level `network` field comes from the
persistent workspace manifest; when a workspace has a runtime state file,
`runtime` shows the last network config recorded by the backend supervisor.
The `create`/`list`/`delete` subcommands manage named networks - user-defined,
VM-independent subnets that workspaces join by name.

## Examples

Inspect a workspace's network:

```bash
microagent --json network research
```

```json
{
  "workspace": "research",
  "state": "running",
  "backend": "firecracker",
  "network": {
    "mode": "nat",
    "portForwards": [
      {
        "protocol": "tcp",
        "host": "127.0.0.1",
        "hostPort": 8080,
        "guestPort": 80
      }
    ]
  },
  "runtime": {
    "mode": "nat",
    "ip": "10.43.12.2/29",
    "subnet": "10.43.12.0/29",
    "gateway": "10.43.12.1",
    "dns": ["1.1.1.1", "8.8.8.8"],
    "routes": ["0.0.0.0/0 via 10.43.12.1"]
  }
}
```

Manage named networks:

```bash
microagent network create frontend
microagent network create backend --subnet 10.99.0.0/24
microagent network list
microagent network delete backend
```

## Named networks

`network create` registers a user-defined network - a VM-independent record
that workspaces can share so they sit on one subnet and can address each other.
A subnet is auto-allocated from `10.44.0.0/16` (one `/24` per network) unless
`--subnet` is given; the gateway is the first usable host. The registry lives at
`<state-dir>/networks/index.json`.

`network delete` fails closed while a network still has members; pass `--force` to
remove it anyway.

## Joining a network

Attach a workspace to a named network with `--network-name` on `create`/`run`:

```bash
microagent network create devnet
microagent create --name web --image docker.io/library/python:3.12 --network-name devnet
microagent create --name db  --image docker.io/library/postgres:16 --network-name devnet
```

Each member is allocated a **stable IP** from the network's subnet (persisted in
the registry, so it survives stop/start). On Firecracker/Linux, members share a
managed Linux bridge so they reach each other directly:

```bash
microagent exec web -- ping db        # resolve by name and reach the peer
```

Name resolution is provided by `/etc/hosts`, injected at boot from the current
member set (the cmdline → guest-init seam, parallel to DNS). Because it is a
boot-time snapshot, a member learns peers that joined **before** it booted;
restart a workspace to pick up members that joined later. Cross-VM connectivity
by IP is always available regardless of boot order. Deleting a workspace frees
its address; the shared bridge is removed once the last member stops.

Named workspace attachment is currently implemented by the Firecracker/Linux
backend. It requires `net.ipv4.ip_forward=1` on the host (as with `nat` mode)
and CAP_NET_ADMIN in the supervisor - see
[`host setup-networking`](/cli/host/#setup-networking). The `network create`
registry commands can run on macOS, but Apple VF does not currently implement
`network.mode=named`; starting an Apple VF workspace on a named network fails
backend validation.

## Flags

You'll rarely need flags here - `--subnet` when the auto-allocated range
collides with something on your host, `--force` for cleanup.

| Flag | Description |
|---|---|
| `--subnet <cidr>` | Subnet for `create`; auto-allocated from `10.44.0.0/16` when omitted |
| `--network-name <name>` | On `create`/`run`: join a workspace to a named network (implies named mode) |
| `--force` | Remove a network even if it still has members |
| `--state-dir <dir>` | State directory holding the workspace and network records (default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Exit status

`network` exits `0` on success; nonzero when the workspace or named network
cannot be found, or when `delete` would remove a network that still has members
(without `--force`). In AX mode a failure is written as a structured error
envelope.

## Related

- [`create`](/cli/create/) - join a network with `--network-name`
- [`status`](/cli/status/) - the same network block in the full status view
- [Networking](/concepts/networking/) - all network modes and what they require
