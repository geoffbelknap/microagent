---
title: microagent network
description: Inspect a workspace's networking.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-23_

```text
microagent network <workspace> [--state-dir <dir>]   Inspect a workspace's network
```

`network` reports the network mode, declared port forwards, DNS servers,
routes, and IP information recorded for a workspace. The top-level `network`
field comes from the persistent workspace manifest; when a workspace has a
runtime state file, `runtime` shows the last network config recorded by the
backend supervisor.

## Examples

Inspect a workspace's network:

```bash
microagent --json network research
```

```json
{
  "workspace": "research",
  "state": "running",
  "backend": "linux-kvm",
  "network": {
    "mode": "user",
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
    "mode": "user",
    "ip": "10.43.12.2/29",
    "subnet": "10.43.12.0/29",
    "gateway": "10.43.12.1",
    "dns": ["1.1.1.1", "8.8.8.8"],
    "routes": ["0.0.0.0/0 via 10.43.12.1"]
  }
}
```

## Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory holding the workspace records (default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Exit status

`network` exits `0` on success; nonzero when the workspace cannot be found. In
AX mode a failure is written as a structured error envelope.

## Related

- [`status`](/cli/status/) - the same network block in the full status view
- [Networking](/concepts/networking/) - the available network modes and what they require
