---
title: microagent network
description: Inspect workspace network intent and runtime network state.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-02_

```text
microagent network <workspace> [--state-dir <dir>]   Inspect a workspace's network
microagent network create <name> [--subnet <cidr>]   Create a named network
microagent network ls                                 List named networks
microagent network rm <name> [--force]                Remove a named network
```

With a workspace name, `network` reports the network mode, bridged host
interface, declared port forwards, DNS servers, routes, and IP information
recorded for that workspace. The top-level `network` field comes from the
persistent workspace manifest; when a workspace has a runtime state file,
`runtime` shows the last network config recorded by the backend supervisor.

## Named networks

`network create` registers a user-defined network — a VM-independent record
that workspaces can share so they sit on one subnet and can address each other.
A subnet is auto-allocated from `10.44.0.0/16` (one `/24` per network) unless
`--subnet` is given; the gateway is the first usable host. The registry lives at
`<state-dir>/networks/index.json`.

```bash
microagent network create frontend
microagent network create backend --subnet 10.99.0.0/24
microagent network ls
microagent network rm backend
```

`network rm` fails closed while a network still has members; pass `--force` to
remove it anyway. Joining a workspace to a network and the resulting cross-VM
connectivity and name resolution are realized by the backend supervisor.

## Flags

| Flag | Description |
|---|---|
| `--subnet <cidr>` | Subnet for `create`; auto-allocated from `10.44.0.0/16` when omitted |
| `--force` | Remove a network even if it still has members |
| `--state-dir <dir>` | State directory holding the workspace and network records (default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

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
