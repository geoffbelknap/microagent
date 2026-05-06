---
title: Firecracker supervisor
description: Linux backend lifecycle through the executable Go supervisor.
---

The Firecracker backend uses the same executable supervisor protocol as Apple
VF. The supervisor is packaged as `microagent-firecracker-supervisor`.

The supervisor:

- validates `vmkit.Request`
- writes `firecracker.json`
- starts `firecracker --no-api --config-file ...`
- records runtime state and PID under `config.stateDir`
- emits `vmkit.Response` lifecycle events
- sends `SIGTERM` for `stop`, `SIGKILL` for `kill`, and does not signal the VM
  process for `quarantine`

## Process Model

Firecracker itself is still a separate VM process. The supervisor executable is
the Go implementation that owns its config, process group, state files, and
lifecycle responses.

Callers can invoke the supervisor directly with a JSON `vmkit.Request`, or use
the `pkg/supervisors/firecracker` Go package.

## State

The Firecracker supervisor writes backend runtime files under:

```text
<state-dir>/<runtimeID>/
```

Important files include:

| File | Purpose |
|---|---|
| `runtime.json` | latest structured lifecycle state |
| `firecracker.json` | generated Firecracker config |
| `serial.log` | guest serial output |
| `serial.in` | console input FIFO for running workspaces |
| transient `magtap*` device | TAP created for bridged mode and removed on quarantine/stop/kill/delete |

Persistent workspace disks live under:

```text
<state-dir>/workspaces/<runtimeID>/
```

## Limitations

- Requires Linux with `/dev/kvm`.
- Uses `/dev/vhost-vsock` for guest-to-host vsock support.
- Requires the `firecracker` binary on `PATH`, under packaged `libexec`, or
  through `MICROAGENT_FIRECRACKER`.
- Supports interactive `microagent connect`; use `microagent logs` to review
  captured serial output.
- Firecracker `network.mode` supports `nat`, `isolated`, and `bridged`.
- Firecracker `bridged` requires `network.interface` to name an existing Linux
  bridge and requires host permissions to create and attach a TAP device.
- The supervisor does not implement a direct `console` command. The CLI uses
  the Firecracker serial input FIFO for `microagent connect`.

## Networking

`nat` preserves the existing Firecracker behavior and live TCP `--publish`
support. Published TCP listeners are still host-side listeners bridged to the
guest through vsock.

`isolated` writes no Firecracker network device and rejects `--publish` before
the supervisor starts the VM.

`bridged` creates a deterministic transient TAP device, attaches it to the
requested Linux bridge, and writes a Firecracker `network-interfaces` entry
using that TAP. The TAP name is recorded in `runtime.json` while running and is
deleted on `quarantine`, `stop`, `kill`, or `delete`. Missing
`network.interface`, a
nonexistent interface, a non-bridge interface, missing `iproute2`, missing
permissions, or TAP setup failure all fail closed with explicit errors.

## Quarantine

Firecracker quarantine preserves the recorded VM PID and does not signal the VM
process. It terminates the host-side port-forwarder, removes transient network
devices, unlinks the workspace vsock socket, and records the state as
`quarantined` in `event.json`, `runtime.json`, and `events.json`.

## Console

Firecracker matches the Apple VF operator-facing console behavior:

- `microagent connect <name> --send "echo CONNECT_READY"` reaches a running
  guest shell and prints `CONNECT_READY`.
- interactive `microagent connect <name>` waits for the guest shell readiness
  gate by default.
- `Ctrl-]` detaches without stopping the workspace.
- errors clearly distinguish "guest shell is not ready" from "console input is
  unavailable".
- serial output remains inspectable through `microagent logs`.

The console gate is:

```bash
make smoke-firecracker-console
```

That target requires Linux amd64 with KVM and is part of the Linux `make smoke`
suite.

`consoleAvailable` in host reports describes backend capability. A prepared
workspace still reports "console input is not ready" until it has been started
and the runtime serial input FIFO exists.
