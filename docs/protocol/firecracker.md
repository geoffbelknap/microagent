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

## Console Parity Target

Firecracker console parity is not complete until it matches the Apple VF
operator-facing contract:

- `microagent connect <name> --send "echo CONNECT_READY"` reaches a running
  guest shell and prints `CONNECT_READY`.
- interactive `microagent connect <name>` waits for the guest shell readiness
  gate by default.
- `Ctrl-]` detaches without stopping the workspace.
- errors clearly distinguish "guest shell is not ready" from "console input is
  unavailable".
- serial output remains inspectable through `microagent logs`.

The tracking gate is:

```bash
make smoke-firecracker-console
```

That target is intentionally not part of default smoke yet because it requires
Linux amd64 with KVM and currently fails until Firecracker serial input support
is implemented.
