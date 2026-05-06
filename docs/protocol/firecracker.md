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

Persistent workspace disks live under:

```text
<state-dir>/workspaces/<runtimeID>/
```

## Limitations

- Requires Linux with `/dev/kvm`.
- Uses `/dev/vhost-vsock` for guest-to-host vsock support.
- Requires the `firecracker` binary on `PATH`, under packaged `libexec`, or
  through `MICROAGENT_FIRECRACKER`.
- Does not support interactive `connect`; use `microagent logs`.
