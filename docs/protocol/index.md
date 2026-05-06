---
title: Supervisor contract
description: The shared request and response shape used by backend supervisors.
---

Microagent treats backend lifecycle work as a supervisor contract. A supervisor
accepts a `vmkit.Request`, performs one lifecycle command, and returns a
`vmkit.Response`.

Firecracker and Apple VF share this contract:

- **Firecracker** implements it as `microagent-firecracker-supervisor`.
- **Apple VF** implements it as the `microagent-applevf-supervisor`
  executable because Virtualization.framework is Swift-only.

Both backend supervisors have the same executable wire protocol. The CLI uses
the active host backend's supervisor.

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
    "cpuCount": 2
  }
}
```

## Commands

| Command | Purpose | Required input |
|---|---|---|
| `host` | Report host support | none |
| `check` | Validate request/config only | identity and full config |
| `prepare` | Write backend state/config without booting | identity and full config |
| `start` | Start a prepared workspace | identity and full config |
| `run` | Start in foreground | identity and full config |
| `console` | Attach to a running console | identity and full config; Apple VF only |
| `inspect` | Read latest state | identity and `config.stateDir` |
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
  }
}
```

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
    "consoleAvailable": false,
    "consoleMode": "serial-log"
  }
}
```

Valid states are `unknown`, `prepared`, `starting`, `running`, `stopping`,
`stopped`, and `failed`.

## Backend Notes

- [Firecracker supervisor](/protocol/firecracker/) documents the Linux
  executable implementation.
- [Apple VF supervisor](/protocol/applevf/) documents the macOS executable
  protocol.
