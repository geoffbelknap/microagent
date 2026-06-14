---
title: Connect workspaces on a named network
description: Put an app and a database on one named network so they reach and resolve each other by name.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-14_

Two workspaces, one subnet: an app called `web` that reaches a database
called `db` by name. That's where this guide ends up. A named network is
microagent's managed analog of a Docker user-defined network: declare it once,
join workspaces by name.

Named-network attachment is currently implemented by the Firecracker/Linux
backend and needs the same host setup as `nat` mode: `net.ipv4.ip_forward=1`
and `CAP_NET_ADMIN` in the supervisor. Check and apply that once:

```bash
microagent host setup-networking --check
microagent host setup-networking
```

The registry commands below run anywhere; only booting members needs the
privileged setup. Apple VF does not currently implement `network.mode=named`.

## 1. Create the network

```bash
microagent network create devnet --subnet 10.44.50.0/24
```

```text
Created network "devnet" (10.44.50.0/24, gateway 10.44.50.1)
```

This is a registry record - no host devices exist until a member starts. Omit
`--subnet` to auto-allocate a `/24` from `10.44.0.0/16`.

## 2. Join the members

`--network-name` on `create` or `run` joins a workspace to the network:

```bash
microagent create db  --image docker.io/library/postgres:16 \
  --network-name devnet --publish 127.0.0.1:5432:5432/tcp
microagent create web --image docker.io/library/python:3.12 \
  --network-name devnet
microagent start db
microagent start web
```

Each member gets a stable address from the subnet (`db` → `10.44.50.2`,
`web` → `10.44.50.3`; `.1` is the gateway), persisted in the registry so it
survives stop and start. The first member to start brings up the shared
managed bridge; later members attach to it. Outbound egress still works,
routed through the gateway with NAT.

Check membership and runtime addresses:

```bash
microagent network ls
microagent --json network web
```

```text
NAME                 SUBNET             GATEWAY         MEMBERS
devnet               10.44.50.0/24      10.44.50.1      2
```

## 3. Reach the peer by name

`/etc/hosts` inside each member is populated at boot from the member set, so
names just work:

```bash
microagent exec web -- ping -c2 db
microagent exec web -- sh -c "nc -z db 5432 && echo db-reachable"
```

```text
db-reachable
```

(The `ping` output is trimmed - shown is the second command's confirmation.)

Point the app's connection string at `db` (for example
`postgres://db:5432/...`) - the name resolves to the peer's stable address.

## 4. Mind the boot order

`/etc/hosts` is a boot-time snapshot: a member knows the peers that existed
when it booted. Start `db` first and `web` resolves it immediately, but `db`
won't have `web` in its hosts file until `db` restarts. Reachability *by IP*
is always order-independent - it's one L2 segment.

To refresh an earlier member after newcomers join, restart it:

```bash
microagent halt db
microagent start db    # db now resolves web too
```

For a set of long-lived members that all need each other's names, start them
all, then do one restart pass - or let each service retry resolution, which
database clients already do.

## Clean up

```bash
microagent halt web && microagent delete web --yes
microagent halt db && microagent delete db --yes
microagent network rm devnet
```

Deleting a workspace frees its address; the managed bridge is reaped when the
last member stops. `network rm` fails closed while members remain - `--force`
overrides.

## What's next

- **All five network modes and the backend matrix** - [Networking](/concepts/networking/).
- **The `network` command surface** - the [`network`](/cli/network/) reference.
- **Publish a member's port to the host** - [run a service](/guides/run-a-service/).
