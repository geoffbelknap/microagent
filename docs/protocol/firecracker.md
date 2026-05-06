---
title: Firecracker supervisor
description: Linux backend lifecycle through the executable Go supervisor.
---

The Firecracker backend uses the same executable supervisor contract as Apple
VF. The supervisor is packaged as `microagent-firecracker-supervisor`.

The supervisor:

- validates `vmkit.Request`
- writes `firecracker.json`
- starts `firecracker --no-api --config-file ...`
- records runtime state and PID under `config.stateDir`
- emits `vmkit.Response` lifecycle events
- sends `SIGTERM` for `stop` and `SIGKILL` for `kill`

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

## Console

Firecracker matches the Apple VF operator-facing console contract:

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
