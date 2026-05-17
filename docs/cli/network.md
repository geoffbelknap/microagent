---
title: microagent network
description: Inspect workspace network intent and runtime network state.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-17_

```text
microagent network <name> [--state-dir <dir>]
```

`network` reports the network mode, bridged host interface, declared port
forwards, DNS servers, routes, and IP information recorded for a workspace. The
top-level `network` field comes from the persistent workspace manifest. When a
workspace has a runtime state file, `runtime` shows the last network config
recorded by the backend supervisor.

## Example

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

## Related

- [`create`](/cli/create/)
- [`status`](/cli/status/)
