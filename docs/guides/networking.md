---
title: Networking
description: Give a workspace outbound access and publish a guest port back to the host.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-13_

A workspace has one of two network modes: `user` (the default) gives the guest
unprivileged outbound IPv4 plus any TCP ports you publish, and `isolated` gives
it no network device at all. For controlling and auditing what the guest may
reach, read [egress mediation](/concepts/egress-mediation/).

## Outbound access (the default)

`user` mode is the default, so a plain workspace can already reach the network:

```bash
microagent create research --image docker.io/library/python:3.12
microagent start research
microagent exec research -- curl -sS https://example.com >/dev/null && echo ok
```

You do not need to configure host routing, bridges, or packet forwarding for
the default path. If outbound networking fails, run `microagent doctor` first;
it checks the host prerequisites for the current platform.

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

## Related

- [Network modes](/concepts/networking/) — both modes in detail.
- [`network`](/cli/network/) — the command reference.
- [Run a service](/guides/run-a-service/) — publish a service's port to the host.
- [Egress mediation](/concepts/egress-mediation/) — control and audit what the guest reaches.
