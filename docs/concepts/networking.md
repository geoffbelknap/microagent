---
title: Networking
description: Declarative workspace network intent.
---

Workspaces carry a network record in their manifest and supervisor request.
The current CLI accepts these modes:

| Mode | Meaning |
|---|---|
| `nat` | Default backend network mode |
| `isolated` | Reserved mode for no external guest network |
| `bridged` | Reserved mode for host-bridge attachment |

Current backend support is narrower than the full enum:

| Backend | Supported mode today |
|---|---|
| Apple VF | `nat` record only; explicit NAT/isolated/bridged device behavior still needs backend work |
| Firecracker | `nat` plus live TCP `--publish`; `isolated` and `bridged` fail closed in the supervisor |

On Apple VF, non-`nat` modes are currently preserved as manifest intent only;
they do not yet enforce guest isolation or attach a bridged host interface.

Create records the mode:

```bash
microagent create research --network nat
```

Port forwards are declared with repeatable `--publish` flags:

```bash
microagent create research --publish 127.0.0.1:8080:80/tcp
```

For TCP forwards on Firecracker, guest init records a `hostForwards` entry and
listens on a guest vsock port matching the declared host port. The Firecracker
supervisor owns the host-side listener that connects host TCP to that guest
vsock port. Apple VF `--publish` still needs backend work.

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

The network record is visible in JSON output from `create`, `start`, `status`,
and `ps`. Backend-specific wiring is intentionally behind the supervisor
contract; malformed port forwards fail closed before a request is sent.
