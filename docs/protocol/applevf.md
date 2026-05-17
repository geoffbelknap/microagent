---
title: Apple VF supervisor protocol
description: One JSON request in, one JSON response out.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-17_

microagent uses the supervisor concept for backend lifecycle work. The
Apple VF supervisor is packaged as a standalone executable,
`microagent-applevf-supervisor`, so non-Swift callers can cross the
Virtualization.framework boundary through a narrow JSON protocol.

`microagent-applevf-supervisor` reads one JSON request from stdin, writes one
JSON response to stdout, and exits. Diagnostics go to stderr. Exit code `0`
means the response has `"ok": true`. Nonzero exit means the response has
`"ok": false` or the request could not be decoded.

For the shared command list and response shape, see
[Supervisor protocol](/protocol/). This page only covers the executable
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
- `halt`
- `quarantine`
- `stop`
- `kill`
- `delete`

`host` does not require `identity` or `config`. `inspect`, `halt`,
`quarantine`, `stop`, `kill`, and `delete` require `identity` and
`config.stateDir`. `check`, `prepare`, `start`, `run`, and `console` require
the full config.

`halt` is a clean disk-preserving stop. `quarantine` is a distinct forensic
state that preserves disk state and event history while severing host-side
network and mediation paths. A quarantined workspace must be halted, stopped,
or killed before it can be started again.

For a running workspace, `quarantine` is handled inside the live Apple VF
supervisor process. The command process sends a control signal and waits for an
acknowledgement before recording `quarantined`. The live supervisor detaches
network attachments, removes virtio-vsock listeners, closes published TCP
listeners, and removes serial input without stopping the VM process.

### Disks

Extra disks are optional. `mode` must be `ro` or `rw`. The supervisor
attaches them after the rootfs in request order; the guest init mounts them
from `/dev/vdb` onward.

### Network

Apple VF maps `config.network.mode` to Virtualization.framework network
devices:

| Mode | Apple VF behavior |
|---|---|
| `nat` | Adds a `VZNATNetworkDeviceAttachment` and requests guest DHCP with `ip=dhcp` |
| `isolated` | Adds no network device |
| `bridged` | Adds a `VZBridgedNetworkDeviceAttachment` and requests guest DHCP with `ip=dhcp` |

`nat` is the default. It uses Virtualization.framework's native NAT service,
not a TAP device, host bridge, or host firewall rule set managed by
Microagent. On supported macOS versions, Apple VF supplies the guest with a
DHCP lease, default route, and DNS service through that native attachment. The
guest kernel config used with Apple VF must support kernel DHCP autoconfig
because Microagent starts `/sbin/microagent-init` directly and should not rely
on image-local DHCP clients such as `dhclient` or `udhcpc`. Guest init writes
`/etc/resolv.conf` from the kernel DHCP nameserver data so slim images can
resolve DNS without extra packages.

Apple does not expose a stable guest IP, gateway, or DNS assignment through the
Virtualization.framework NAT API. Status and network responses therefore report
the requested mode and declared TCP publishes, but Apple VF NAT runtime details
do not include deterministic `ip`, `gateway`, or `dns` values. Treat the NAT
assignment as backend-managed. If a guest cannot resolve DNS or reach outbound
TCP targets, check that the host is online, that the guest kernel supports
`ip=dhcp`, and that host firewall or endpoint-security software is not blocking
the supervisor process.

`bridged` requires `config.network.interface`, matched against the Apple VF
bridged interface identifier or localized display name. It also requires the
supervisor process to have Apple's restricted `com.apple.vm.networking`
entitlement. Open-source builds cannot self-sign that entitlement, and `sudo`
does not bypass the check. Local ad-hoc builds fail closed during `check` with
a clear entitlement error.

Port forwards are supported for TCP. The supervisor listens on the requested
host address and port, connects to the guest over virtio-vsock, and guest init
proxies the stream to the requested guest TCP port.

### Mediation

`config.mediation` uses the backend-neutral shape:

```json
{
  "enabled": true,
  "required": true,
  "port": 2048,
  "target": "127.0.0.1:9900",
  "failClosed": true
}
```

Required mediation fails closed: `failClosed` must be `true`, and enabled
mediation must declare a port and `host:port` target. The supervisor installs
mediation as a guest-to-host virtio-vsock listener and forwards accepted
connections to that target.

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
  },
  "readiness": {
    "guestReady": {},
    "shellReady": {},
    "resultReady": {},
    "mediationReady": {}
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
