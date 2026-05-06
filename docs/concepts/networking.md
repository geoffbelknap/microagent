---
title: Networking
description: Declarative workspace network intent.
---

Workspaces carry a declarative network record in their manifest and supervisor
request. The current modes are:

| Mode | Meaning |
|---|---|
| `nat` | Default outbound-capable network intent |
| `isolated` | No external network intent |
| `bridged` | Host-network bridge intent |

Create records the mode:

```bash
microagent create research --network nat
```

Port forwards are declared with repeatable `--publish` flags:

```bash
microagent create research --publish 127.0.0.1:8080:80/tcp
```

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
contract; invalid network modes and malformed port forwards fail closed before
a request is sent.
