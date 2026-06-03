---
title: Connect two workspaces on a named network
description: Put two microVMs on one named network so they reach and resolve each other by name.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-03_

This recipe wires two workspaces together on a [named network](/concepts/networking/#named-networks)
so they share a subnet, reach each other directly, and resolve each other by
name. The example is a classic split: an app workspace (`web`) talking to a
database workspace (`db`).

Named workspace attachment is currently implemented by the Firecracker/Linux
backend and needs the same host setup as `nat` mode:
`net.ipv4.ip_forward=1` and `CAP_NET_ADMIN` in the supervisor (run as root, or
grant `cap_net_admin,cap_setpcap+ep`). The named-network registry commands can
run on macOS, but Apple VF does not currently implement `network.mode=named`,
so this recipe does not apply to Apple VF workspaces.

## 1. Create the network

```bash
microagent network create devnet --subnet 10.44.50.0/24
```

This is just a registry record — no host devices are created until a member
starts. Omit `--subnet` to auto-allocate a `/24` from `10.44.0.0/16`.

## 2. Start the members

```bash
microagent create db  --image docker.io/library/postgres:16 \
  --network-name devnet --publish 127.0.0.1:5432:5432/tcp
microagent create web --image docker.io/library/python:3.12 \
  --network-name devnet
```

Each member is allocated a stable address from the subnet (`db` → `10.44.50.2`,
`web` → `10.44.50.3`, since `.1` is the gateway). The first member to start
brings up the shared bridge `mbr<hash>` with the gateway address; later members
attach their TAP to it.

Check membership:

```bash
microagent network ls
microagent --json network web        # runtime IP/subnet/gateway for this member
```

## 3. Reach the peer

From `web`, reach `db` by name — `/etc/hosts` was populated at boot from the
member set:

```bash
microagent exec web -- ping -c2 db
microagent exec web -- sh -c 'nc -z db 5432 && echo db-reachable'
```

Point your app's connection string at `db` (e.g. `postgres://db:5432/...`) — the
name resolves to the peer's stable address, which is stable across stop/start.

## The boot-order nuance

`/etc/hosts` is a **boot-time snapshot**. A member knows the peers that existed
when it booted, so start order matters for *name* resolution:

- If you start `db` first, then `web`, then `web` resolves `db` immediately, but
  `db` won't have `web` until it restarts.
- Reachability *by IP* is always available regardless of order (it's L2 over the
  shared bridge).

To refresh an earlier member after newcomers join, restart it:

```bash
microagent stop db && microagent start db    # db now resolves web too
```

For long-lived members that all need to resolve each other, start them, then do
one restart pass — or have each service retry name resolution, which most
database clients already do.

## Clean up

```bash
microagent delete web --yes
microagent delete db --yes          # frees both addresses
microagent network rm devnet        # the shared bridge is already reaped
```

Deleting a workspace frees its address in the registry; the managed bridge is
removed automatically once the last member stops. `network rm` fails closed if
members remain — pass `--force` to override.

## See also

- [Networking → Named networks](/concepts/networking/#named-networks)
- [`microagent network`](/cli/network/)
