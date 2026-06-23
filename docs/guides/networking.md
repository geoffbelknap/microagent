---
title: Networking
description: Give a workspace outbound access and publish a guest port back to the host.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-23_

A workspace has one of two network modes: `user` (the default) gives the guest
unprivileged outbound IPv4 plus any TCP ports you publish, and `isolated` gives
it no network device at all. This guide covers the common `user`-mode tasks. For
the mechanics under each backend, read [networking concepts](/concepts/networking/);
for controlling and auditing what the guest may reach, read
[egress mediation](/concepts/egress-mediation/).

## Outbound access (the default)

`user` mode is the default, so a plain workspace can already reach the network:

```bash
microagent create research --image docker.io/library/python:3.12
microagent start research
microagent exec research -- curl -sS https://example.com >/dev/null && echo ok
```

On Firecracker/Linux the workspace runs inside its own unprivileged user
namespace, with `pasta` providing outbound NAT in user space - no host `setcap`,
`ip_forward`, or bridge setup. On Apple VF/macOS it uses the native
Virtualization.framework NAT attachment.

## Publish a guest port to the host

Use `--publish` to expose a guest TCP port on the host. Repeat it per port:

```bash
microagent create web --image docker.io/library/python:3.12 \
  --publish 127.0.0.1:8080:80/tcp
microagent start web
curl -sS http://127.0.0.1:8080/
```

The host listens on the declared address and port, the supervisor bridges the
connection over the backend's transport, and guest init forwards it to the
requested guest port. See [run a service](/guides/run-a-service/) for a worked
example.

## No network at all

When a workspace should have no network access, use `isolated`:

```bash
microagent create offline --image docker.io/library/python:3.12 --network isolated
```

Isolated workspaces reject `--publish` before the request leaves the CLI -
there's no guest network for a forward to reach.

## What's next

- **Both network modes and the backend matrix** - [Networking](/concepts/networking/).
- **The `network` command surface** - the [`network`](/cli/network/) reference.
- **Publish a service's port to the host** - [run a service](/guides/run-a-service/).
- **Control and audit what the guest reaches** - [egress mediation](/concepts/egress-mediation/).
