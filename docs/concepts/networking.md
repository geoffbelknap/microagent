---
title: Networking
description: Declarative workspace network intent.
---

Workspaces carry a network record in their manifest and supervisor request.
The current CLI accepts these modes:

| Mode | Meaning |
|---|---|
| `nat` | Default backend network mode |
| `isolated` | No guest network device |
| `bridged` | Host bridge attachment; requires a backend-supported host interface |

Current backend support is narrower than the full enum:

| Backend | Supported mode today |
|---|---|
| Apple VF | `nat`, `isolated`, and TCP `--publish`; `bridged` is implemented but blocked in open-source builds by Apple's restricted `com.apple.vm.networking` entitlement |
| Firecracker | `nat` plus live TCP `--publish`; `isolated`; `bridged` through a host Linux bridge |

Apple puts native Apple VF bridged networking behind the restricted
`com.apple.vm.networking` entitlement. Open-source builds cannot self-sign that
entitlement, and running the supervisor with `sudo` does not change the check.

Create records the mode:

```bash
microagent create research --network nat
```

Port forwards are declared with repeatable `--publish` flags:

```bash
microagent create research --publish 127.0.0.1:8080:80/tcp
```

For TCP forwards, guest init records a `hostForwards` entry and listens on a
guest vsock port matching the declared host port. Backend supervisors own the
host-side listener that connects host TCP to that guest vsock port.

The same shape is available in `microagent.yaml`:

```yaml
network:
  mode: nat
  forwards:
    - host: 127.0.0.1
      hostPort: 8080
      guestPort: 80
      protocol: tcp
```

For bridged Apple VF workspaces, declare the host interface identifier or
display name. The supervisor also needs Apple's restricted
`com.apple.vm.networking` entitlement. Local ad-hoc builds fail closed before
start with an error that names the Apple restriction.

```yaml
network:
  mode: bridged
  interface: en0
```

For bridged Firecracker workspaces, `interface` must name an existing Linux
bridge. The supervisor creates a transient TAP device, attaches it to that
bridge, configures the generated `firecracker.json` network device, and removes
the TAP when the workspace stops, is killed, or is deleted. Missing `iproute2`,
missing privileges, non-bridge interfaces, and TAP setup failures fail closed.

```yaml
network:
  mode: bridged
  interface: br0
```

For isolated workspaces, port forwards are invalid because no guest network is
attached.

The network record is visible in JSON output from `create`, `start`, `status`,
and `ps`. Backend-specific wiring is intentionally behind the supervisor
protocol; malformed port forwards fail closed before a request is sent.
