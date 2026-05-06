---
title: microagent network
description: Inspect workspace network intent and runtime network state.
---

```text
microagent network <name> [--state-dir <dir>]
```

`network` reports the network mode, declared port forwards, DNS servers,
routes, and IP information recorded for a workspace. The top-level `network`
field comes from the persistent workspace manifest. When a workspace has a
runtime state file, `runtime` shows the last network config recorded by the
backend supervisor.

## Example

```bash
microagent network research --json
```

```json
{
  "workspace": "research",
  "state": "running",
  "backend": "apple-vf",
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
  }
}
```

## Related

- [`create`](/cli/create/)
- [`status`](/cli/status/)
