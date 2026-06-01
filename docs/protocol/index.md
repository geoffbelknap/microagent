---
title: Supervisor protocol
description: The JSON request and response format used by backend supervisors.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

Backend supervisors speak a small JSON protocol: one request in, one response
out. A request names a lifecycle command such as `prepare`, `start`, or `stop`.
The response reports whether it worked and, when the command changes VM state,
includes a lifecycle event.

Firecracker, Apple VF, and Windows Hyper-V use the same backend-neutral
protocol:

- **Firecracker** implements it as `microagent-firecracker-supervisor`.
- **Apple VF** implements it as the `microagent-applevf-supervisor`
  executable because Virtualization.framework is Swift-only.
- **Windows Hyper-V** implements it inside the Go supervisor boundary and uses
  HCS for Linux guests without WSL or QEMU.

The CLI chooses the active host backend and sends the request to that
supervisor.

Use [`microagent --json contract`](/cli/contract/) for the versioned
backend-neutral runtime contract.

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
    },
    "mediation": {
      "enabled": true,
      "required": true,
      "port": 2048,
      "target": "127.0.0.1:9900",
      "failClosed": true
    }
  }
}
```

`config.network.mode` is declarative and must be `nat`, `isolated`, or
`bridged`. Bridged backends may require `config.network.interface`. Port
forwards use `protocol`, optional `host`, `hostPort`, and `guestPort`.
`config.mediation` declares the Body-to-host control-plane channel. Required
mediation must set `failClosed: true`; if the channel is unavailable or broken,
consumers should treat it as closed by default.

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
| `quarantine` | Sever host-side network and mediation without stopping the guest | identity and `config.stateDir` |
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
    "execReady": {
      "ready": true,
      "detail": "exec service round-trip ready at 127.0.0.1:42000 in 5ms"
    },
    "resultReady": {
      "ready": false
    },
    "mediationReady": {
      "ready": true,
      "observedAt": "2026-05-02T00:00:00Z",
      "detail": "mediation required=true failClosed=true port=2048 target=127.0.0.1:9900; mediation target reachable at 127.0.0.1:9900 in 1ms"
    }
  },
  "result": {
    "identity": {
      "requestID": "req-1",
      "runtimeID": "agent-1",
      "role": "workload",
      "backend": "firecracker"
    },
    "backend": "firecracker",
    "resultPath": "/tmp/microagent/agent-1/result.json",
    "startedAt": "2026-05-02T00:00:00Z",
    "completedAt": "2026-05-02T00:00:01Z",
    "exitCode": 0
  },
  "artifacts": {
    "ingress": [
      {
        "name": "config",
        "path": "./config.tar",
        "mountpoint": "/config",
        "mode": "ro",
        "kind": "bundle"
      }
    ],
    "egress": [
      {
        "name": "report",
        "path": "/workspace/report.json",
        "kind": "output"
      }
    ]
  },
  "mediation": {
    "enabled": true,
    "required": true,
    "port": 2048,
    "target": "127.0.0.1:9900",
    "failClosed": true
  }
}
```

For named workspace status responses, `verification` compares current runtime
artifacts with the values recorded when the workspace was created. Kernel and
injected-init hashes are always compared. Rootfs hashes are compared while the
workspace is still `prepared`; after start, the rootfs is a writable VM disk, so
status reports current and recorded rootfs hashes without treating normal guest
writes as divergence. If an enforced hash no longer matches,
`verification.ok` is false and `verification.divergence` contains entries with
`artifact`, `field`, `expected`, and `actual`.

`readiness` gives consumers explicit guest, shell, exec, result, and mediation
readiness signals. Each signal has `ready` plus optional `observedAt`, `detail`,
and `error`. `mediationReady` is based on a bounded live TCP reachability probe
to the declared mediation target when the workspace is running. Optional
mediation reports target failures as not ready without a hard `error`; required
mediation reports target failures with `error`.

`mediation` reports the declared guest-to-host vsock channel separately from
ordinary networking and logs. It is the control-plane path between the guest
body and the host process handling work.

When the guest result channel has completed, status responses may include
`result`. The result is structured separately from serial logs and carries
identity, backend, result path, start/completion timestamps, exit code, stdout,
stderr, and guest-reported error.

`artifacts` reports declared input bundles and output paths. These declarations
are persisted with the workspace manifest and are independent of serial logs.
The CLI `artifacts get` command retrieves declared egress artifacts by name
from the rootfs or matching attached disk mountpoint without entering the
workspace.

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
`halted`, `quarantined`, `stopped`, and `failed`. `halted` is a terminal
runtime state where the VM process is gone but the workspace disk, identity,
and event history are preserved for a later `start`. `quarantined` preserves
disk state and event history while severing host-side network, mediation, and
side-effect paths.

For the visual state machine - including which transitions `start`, `halt`, `quarantine`, `stop`, `kill`, and `delete` allow - see [State and identity](/concepts/state-and-identity/).

## Field presence by command

The response above shows every block the protocol can carry. Most commands
return a subset:

| Command | `event` | `host` | `verification` | `readiness` | `result` | `artifacts` | `mediation` |
|---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| `host` | - | ✓ | - | - | - | - | - |
| `check` | ✓ | - | ✓ | - | - | - | declared |
| `prepare` | ✓ | - | ✓ | - | - | declared | declared |
| `start` / `run` | ✓ | - | ✓ | ✓ | conditional | declared | declared |
| `console` | ✓ | - | - | - | - | - | - |
| `inspect` | ✓ | - | ✓ | ✓ | conditional | declared | declared |
| `halt` / `quarantine` / `stop` / `kill` / `delete` | ✓ | - | - | - | - | - | - |

Reading the table:

- **`event`** - present on every command except `host`. Carries identity, state, and `observedAt`.
- **`host`** - only on the `host` command. Reports backend capability, virtualization availability, supervisor path, kernel and console status.
- **`verification`** - present whenever the request touched the rootfs or runtime artifacts. Compares recorded vs current SHAs and reports any divergence.
- **`readiness`** - present after `start` / `run` and on `inspect` of a running workspace. Carries `guestReady`, `shellReady`, `execReady`, `resultReady`, and `mediationReady`.
- **`result`** - present when the guest result file has been delivered. *Conditional* means: included if it exists at the time of the response, omitted otherwise. Don't assume it's there.
- **`artifacts`** - present whenever the workspace declared `bundles` or `outputs`. *Declared* means: included only if the workspace's manifest declares them.
- **`mediation`** - present whenever the workspace declared a mediation channel.

`ok` and `backend` appear on every response.

## Backend pages

- [Firecracker supervisor](/protocol/firecracker/) documents the Linux
  executable implementation.
- [Apple VF supervisor](/protocol/applevf/) documents the macOS executable
  protocol.
- [Windows Hyper-V supervisor](/protocol/windows-hyperv/) documents the
  experimental Windows HCS backend.
- [Runtime contract](/protocol/runtime-contract/) documents the shared
  agent-runtime semantics.
