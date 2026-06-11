---
title: Firecracker supervisor
description: Run the Linux backend - process model, state files, networking, snapshots.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

Read this page when you need to know what the Firecracker supervisor - the
Linux backend - does on the host: which files it writes, how each network mode
works, and what pause/resume and snapshots do underneath. The Firecracker
backend uses the same executable supervisor protocol as Apple VF. The
supervisor is packaged as `microagent-firecracker-supervisor`.

For the shared command list and response shape, see
[Supervisor protocol](/protocol/). This page covers the Linux host behavior and
current limitations.

The supervisor:

- validates `vmkit.Request`
- writes `firecracker.json`
- starts `firecracker --api-sock ... --config-file ...` - the config file boots
  the VM and the API socket stays open so pause/resume and snapshot can control
  the running VM (a snapshot restore/fork instead launches with just the API
  socket and loads the snapshot over it)
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
| `firecracker-api.sock` | Firecracker API socket for pause/resume/snapshot control |
| `serial.log` | guest serial output |
| `serial.in` | console input FIFO for running workspaces |
| `snapshots/<tag>/` | snapshot artifacts: `vmstate`, `memory`, `rootfs.ext4`, `manifest.json` |
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

## Pause/resume and snapshots

Because the VM boots with its API socket open, the supervisor controls the
running VM over it (`pauseResumeAvailable` and `snapshotAvailable` are `true` in
Firecracker host reports):

- `pause`/`resume` issue `PATCH /vm` (`Paused`/`Resumed`) and record `paused`/
  `running`, keeping the VM process and host-side aux processes alive.
- `snapshot` auto-pauses a running VM (recorded in the event history), issues
  `PUT /snapshot/create`, copies the workspace `rootfs.ext4` while paused so
  memory and disk are coherent, writes `manifest.json`, and resumes. An
  already-paused workspace is snapshotted in place.
- `start --from-snapshot <tag>` restores in place: it rolls the rootfs back to
  the snapshot's copy, launches `firecracker --api-sock`, and `PUT
  /snapshot/load` with `resume_vm`. The snapshot's baked kernel hash must match.
- `create --from-snapshot <ws>:<tag>` forks a new workspace. Because a snapshot
  bakes the source's vsock socket path, a fork launches Firecracker in a
  per-fork mount namespace that bind-mounts the fork's directory over the
  source's, takes its own host-side service ports (bridged to the guest's
  snapshot ports), and remaps the restored network device with Firecracker
  `network_overrides`. Concurrent networked forks use `user` mode (each fork in
  its own pasta namespace); a `nat` fork is single-instance.

Restoring or forking re-establishes host networking fresh: in-flight guest
connections (outbound TCP, exec/shell/mediation vsock) reset and the guest body
must reconnect.

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
