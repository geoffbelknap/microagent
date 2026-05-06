---
title: Apple VF supervisor protocol
description: One JSON request in, one JSON response out.
---

Microagent uses the supervisor concept for backend lifecycle work. The Apple VF
supervisor is packaged as a standalone executable,
`microagent-applevf-supervisor`, so non-Swift callers can cross the
Virtualization.framework boundary through a narrow JSON protocol.

`microagent-applevf-supervisor` reads one JSON request from stdin, writes one
JSON response to stdout, and exits. Diagnostics go to stderr. Exit code `0`
means the response has `"ok": true`. Nonzero exit means the response has
`"ok": false` or the request could not be decoded.

For the backend-independent command list and response shape, see
[Supervisor contract](/protocol/). This page only covers the executable
Apple VF process boundary.

## Request

```json
{
  "command": "prepare",
  "identity": {
    "requestID": "req-1",
    "runtimeID": "agent-1",
    "role": "workload",
    "backend": "apple-vf"
  },
  "config": {
    "kernelPath": "/tmp/kernel",
    "rootfsPath": "/tmp/rootfs.ext4",
    "stateDir": "/tmp/microagent",
    "memoryMiB": 512,
    "cpuCount": 2,
    "network": {
      "mode": "nat"
    },
    "disks": [
      {
        "name": "config",
        "path": "/tmp/config.ext4",
        "mountpoint": "/config",
        "mode": "ro"
      }
    ]
  }
}
```

### Commands

- `host`
- `check`
- `prepare`
- `run`
- `start`
- `console`
- `inspect`
- `stop`
- `kill`
- `delete`

`host` does not require `identity` or `config`. `inspect`, `stop`, `kill`,
and `delete` require `identity` and `config.stateDir`. `check`, `prepare`,
`start`, `run`, and `console` require the full config.

### Disks

Extra disks are optional. `mode` must be `ro` or `rw`. The supervisor
attaches them after the rootfs in request order; the guest init mounts them
from `/dev/vdb` onward.

### Network

Apple VF maps `config.network.mode` to Virtualization.framework network
devices:

| Mode | Apple VF behavior |
|---|---|
| `nat` | Adds a `VZNATNetworkDeviceAttachment` |
| `isolated` | Adds no network device |
| `bridged` | Adds a `VZBridgedNetworkDeviceAttachment` |

`bridged` requires `config.network.interface`, matched against the Apple VF
bridged interface identifier or localized display name. It also requires the
supervisor process to have Apple's restricted `com.apple.vm.networking`
entitlement. Open-source builds cannot self-sign that entitlement, and `sudo`
does not bypass the check. Local ad-hoc builds fail closed during `check` with
a clear entitlement error. Apple VF port forwards are not implemented yet;
requests with `network.portForwards` fail closed.

## Response

```json
{
  "ok": true,
  "backend": "apple-vf",
  "event": {
    "identity": {
      "requestID": "req-1",
      "runtimeID": "agent-1",
      "role": "workload",
      "backend": "apple-vf"
    },
    "state": "prepared",
    "observedAt": "2026-05-02T00:00:00Z"
  }
}
```

Host responses use `host` instead of `event`:

```json
{
  "ok": true,
  "backend": "apple-vf",
  "host": {
    "backend": "apple-vf",
    "architecture": "arm64",
    "frameworkAvailable": true,
    "virtualizationSupported": true,
    "supervisorPath": "/usr/local/bin/microagent-applevf-supervisor",
    "supervisorAvailable": true,
    "consoleAvailable": true,
    "consoleMode": "interactive"
  }
}
```

## Calling the supervisor directly

The supervisor is invokable from any language with a JSON parser. From shell:

```bash
echo '{"command":"host"}' | microagent-applevf-supervisor
```

From Go, Python, Rust, or Node, spawn the binary, write the request to
stdin, and parse stdout. The CLI does this through `pkg/vmkit`; integrators
can either reuse that package or speak the protocol directly.
