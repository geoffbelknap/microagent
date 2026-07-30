---
title: Networking
description: Give a workspace outbound access and publish a guest port back to the host.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-30_

A workspace has one of two network modes: `user` (the default) gives the guest
unprivileged outbound IPv4 plus any TCP ports you publish, and `isolated` gives
it no network device at all. For controlling and auditing what the guest may
reach, read [egress mediation](/concepts/egress-mediation/).

## Outbound access (the default)

`user` mode is the default, so a plain workspace can already reach the network:

```bash
microagent create research --image docker.io/library/python:3.12-slim
microagent start research
microagent exec research -- python3 -c \
  "import urllib.request; print(urllib.request.urlopen('https://example.com').status)"
```

```text
200
```

You do not need to configure host routing, bridges, or packet forwarding for
the default path. If outbound networking fails, run `microagent doctor` first;
it checks the host prerequisites for the current platform.

## Publish a guest port to the host

Use `--publish` to expose a guest TCP port on the host. Repeat it per port.
The guest needs something listening on the published port, so give the
workspace a service command:

```bash
microagent create web --image docker.io/library/python:3.12-slim \
  --service-command "python3 -m http.server 8000" \
  --publish 127.0.0.1:8080:8000/tcp
microagent start web
curl -sS http://127.0.0.1:8080/ | head -3
```

```text
<!DOCTYPE HTML>
<html lang="en">
<head>
```

The host listens on the declared address and port, the supervisor bridges the
connection over the backend's transport, and guest init forwards it to the
requested guest port. See [run a service](/guides/run-a-service/) for a worked
example with a named volume and restart policy.

## No network at all

When a workspace should have no network access, use `isolated`:

```bash
microagent create offline --image docker.io/library/python:3.12-slim --network isolated
```

Isolated workspaces reject `--publish` before the request leaves the CLI -
there's no guest network for a forward to reach.

## Clean up

```bash
microagent delete research --yes
microagent delete web --yes
microagent delete offline --yes
```

## Related

- [Network modes](/concepts/networking/) — both modes in detail.
- [`network`](/cli/network/) — the command reference.
- [Run a service](/guides/run-a-service/) — publish a service's port to the host.
- [Egress mediation](/concepts/egress-mediation/) — control and audit what the guest reaches.
