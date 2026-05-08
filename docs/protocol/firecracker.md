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

## Process model

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
| `runtime.json` | latest lifecycle state |
| `firecracker.json` | generated Firecracker config |
| `serial.log` | guest serial output |
| `serial.in` | console input FIFO for running workspaces |
| transient `magtap*` device | TAP created for `nat` or `bridged` mode and removed on quarantine/stop/kill/delete |

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
- Firecracker `network.mode` supports `user`, `nat`, `isolated`, and `bridged`.
- Firecracker `user` requires `pasta`, unprivileged user namespaces, and
  `/dev/net/tun`.
- Firecracker `nat` requires nftables-capable kernel support, host IPv4
  forwarding, and permission to create TAP devices and edit firewall rules.
- Firecracker `bridged` requires `network.interface` to name an existing Linux
  bridge and requires host permissions to create and attach a TAP device.
- The supervisor does not implement a direct `console` command. The CLI uses
  the Firecracker serial input FIFO for `microagent connect`.

## Networking

`user` re-execs the supervisor under `pasta`, which creates an unprivileged
user and network namespace. Inside that namespace, the supervisor creates the
Firecracker TAP, configures namespace-local forwarding, and starts Firecracker.
Pasta bridges the namespace to the host network without host capabilities.

`nat` creates a deterministic transient TAP device, allocates a private
`10.43.x.0/29` subnet, assigns the host side to `10.43.x.1`, attaches the TAP
as guest `eth0`, and installs per-workspace nftables rules through
microagent-owned NAT and forward chains. Guest-init reads the assigned network
metadata from the kernel command line, configures `eth0` with a static IPv4
address, installs the default route through the TAP gateway, and writes DNS
resolvers. Published TCP listeners are still host-side listeners bridged to the
guest through vsock.

The assigned runtime IP, subnet, gateway, DNS, and route are recorded in the
runtime network config. The supervisor fails closed if nftables support,
`net.ipv4.ip_forward=1`, or `CAP_NET_ADMIN`-equivalent privileges are missing.

`isolated` writes no Firecracker network device and rejects `--publish` before
the supervisor starts the VM.

`bridged` creates a deterministic transient TAP device, attaches it to the
requested Linux bridge, and writes a Firecracker `network-interfaces` entry
using that TAP. The TAP name is recorded in `runtime.json` while running and is
deleted on `quarantine`, `stop`, `kill`, or `delete`. Missing
`network.interface`, a
nonexistent interface, a non-bridge interface, missing permissions, or TAP
setup failure all fail closed with explicit errors.

## Quarantine

Firecracker quarantine preserves the recorded VM PID and does not signal the VM
process. It terminates the host-side port-forwarder, removes transient network
devices, unlinks the workspace vsock socket, and records the state as
`quarantined` in `event.json`, `runtime.json`, and `events.json`.

## Console

Firecracker matches the Apple VF console behavior callers see:

- `microagent connect <name> --send "echo CONNECT_READY"` reaches a running
  guest shell and prints `CONNECT_READY`.
- interactive `microagent connect <name>` waits for the guest shell readiness
  signal by default.
- `Ctrl-]` detaches without stopping the workspace.
- errors clearly distinguish "guest shell is not ready" from "console input is
  unavailable".
- serial output remains inspectable through `microagent logs`.

`consoleAvailable` in host reports describes backend capability. A prepared
workspace still reports "console input is not ready" until it has been started
and the runtime serial input FIFO exists.
