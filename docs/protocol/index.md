---
title: Supervisor protocol
description: The JSON request and response format used by backend supervisors.
---

Backend supervisors speak a small JSON protocol: one request in, one response
out. A request names a lifecycle command such as `prepare`, `start`, or `stop`.
The response reports whether it worked and, when the command changes VM state,
includes a lifecycle event.

Firecracker and Apple VF use the same protocol:

- **Firecracker** implements it as `microagent-firecracker-supervisor`.
- **Apple VF** implements it as the `microagent-applevf-supervisor`
  executable because Virtualization.framework is Swift-only.

The CLI chooses the active host backend and sends the request to that
supervisor.

## Request

```json
{
  "command": "prepare",
  "identity": {
    "requestID": "req-1",
    "runtimeID": "agent-1",
    "role": "workload",
    "backend": "firecracker"
  },
  "config": {
    "kernelPath": "/tmp/Image",
    "rootfsPath": "/tmp/rootfs.ext4",
    "stateDir": "/tmp/microagent",
    "memoryMiB": 512,
    "cpuCount": 2,
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
}
```

`config.network.mode` is declarative and must be `nat`, `isolated`, or
`bridged`. Bridged backends may require `config.network.interface`. Port
forwards use `protocol`, optional `host`, `hostPort`, and `guestPort`.

## Commands

| Command | Purpose | Required input |
|---|---|---|
| `host` | Report host support | none |
| `check` | Validate request/config only | identity and full config |
| `prepare` | Write backend state/config without booting | identity and full config |
| `start` | Start a prepared workspace | identity and full config |
| `run` | Start in foreground | identity and full config |
| `console` | Attach to a running console | identity and full config |
| `inspect` | Read latest state | identity and `config.stateDir` |
| `halt` | Clean disk-preserving shutdown | identity and `config.stateDir` |
| `stop` | Graceful stop | identity and `config.stateDir` |
| `kill` | Hard stop | identity and `config.stateDir` |
| `delete` | Remove backend runtime state | identity and `config.stateDir` |

## Response

```json
{
  "ok": true,
  "backend": "firecracker",
  "event": {
    "identity": {
      "requestID": "req-1",
      "runtimeID": "agent-1",
      "role": "workload",
      "backend": "firecracker"
    },
    "state": "prepared",
    "observedAt": "2026-05-02T00:00:00Z"
  },
  "verification": {
    "ok": true,
    "imageRef": "docker.io/library/busybox:1.36",
    "resolvedRef": "docker.io/library/busybox@sha256:...",
    "imageDigest": "sha256:...",
    "kernel": {
      "path": "/tmp/Image",
      "sha256": "..."
    },
    "rootfs": {
      "path": "/tmp/rootfs.ext4",
      "sha256": "...",
      "recordedSHA256": "..."
    }
  },
  "readiness": {
    "guestReady": {
      "ready": true,
      "observedAt": "2026-05-02T00:00:00Z",
      "detail": "workspace reached runtime state running"
    },
    "shellReady": {
      "ready": true,
      "detail": "console input is available"
    },
    "resultReady": {
      "ready": false
    }
  }
}
```

For named workspace status responses, `verification` compares current runtime
artifacts with the values recorded when the workspace was created. If a hash no
longer matches, `verification.ok` is false and `verification.divergence`
contains entries with `artifact`, `field`, `expected`, and `actual`.

`readiness` gives consumers explicit guest, shell, and result readiness signals.
Each signal has `ready` plus optional `observedAt`, `detail`, and `error`.

Host responses use `host` instead of `event`.

```json
{
  "ok": true,
  "backend": "firecracker",
  "host": {
    "backend": "firecracker",
    "architecture": "amd64",
    "binaryPath": "/usr/local/bin/firecracker",
    "kvmAvailable": true,
    "vsockAvailable": true,
    "consoleAvailable": true,
    "consoleMode": "interactive"
  }
}
```

Valid states are `unknown`, `prepared`, `starting`, `running`, `stopping`,
`halted`, `stopped`, and `failed`. `halted` is a terminal runtime state where
the VM process is gone but the workspace disk, identity, and event history are
preserved for a later `start`.

## Backend Pages

- [Firecracker supervisor](/protocol/firecracker/) documents the Linux
  executable implementation.
- [Apple VF supervisor](/protocol/applevf/) documents the macOS executable
  protocol.
