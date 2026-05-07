---
title: Networking
description: Declarative workspace network intent.
---

Every workspace records its network intent in the manifest. The CLI accepts three modes:

| Mode | What it does |
|---|---|
| `nat` | Default. Outbound traffic via the backend's NAT, plus declared TCP `--publish` forwards. |
| `isolated` | No guest network device. The body has no network access at all. |
| `bridged` | Attach to a host bridge so the workspace gets its own L2 presence. Backend support varies. |

Backend support is narrower than the enum:

| Backend | What works today |
|---|---|
| Apple VF | `nat`, `isolated`, and TCP `--publish`. `bridged` is implemented but blocked in open-source builds by Apple's restricted `com.apple.vm.networking` entitlement. |
| Firecracker | `nat` plus live TCP `--publish`, `isolated`, and `bridged` through a host Linux bridge. |

Apple gates native bridged networking behind `com.apple.vm.networking`. Open-source builds can't self-sign that entitlement, and `sudo` doesn't bypass the check. If you need bridged on macOS, you sign with the entitlement; otherwise, use `nat`.

## Declaring the mode

```bash
microagent create research --network nat
```

Or in the spec:

```yaml
network:
  mode: nat
  forwards:
    - host: 127.0.0.1
      hostPort: 8080
      guestPort: 80
      protocol: tcp
```

## Port forwards (`--publish`)

Repeat `--publish` for each TCP forward you need:

```bash
microagent create research --publish 127.0.0.1:8080:80/tcp
```

Under the hood, the guest init listens on a vsock port matching the host port; the backend supervisor runs the host-side TCP listener and bridges connections to that vsock port. You don't have to configure either side — declaring the forward wires it up.

Isolated workspaces reject port forwards before the request leaves the CLI: there's no guest network for them to reach.

## Bridged on Apple VF

Declare the host interface identifier or its localized display name:

```yaml
network:
  mode: bridged
  interface: en0
```

The supervisor needs the `com.apple.vm.networking` entitlement. Local ad-hoc builds fail closed during `check` with an error that names the Apple restriction — you'll see it before any VM tries to start.

## Bridged on Firecracker

`interface` must name an existing Linux bridge:

```yaml
network:
  mode: bridged
  interface: br0
```

The supervisor creates a transient TAP device, attaches it to the bridge, writes the Firecracker network device config, and removes the TAP when the workspace is quarantined, stopped, killed, or deleted. Missing `iproute2`, missing privileges, non-bridge interfaces, and TAP setup failures all fail closed.

## Mediation channel

Mediation is a separate guest-to-host vsock contract for the body's calls into the host control plane — distinct from ordinary networking. Declare it with:

```bash
microagent create research --mediation 2048=127.0.0.1:9900
```

By default the channel is required and fail-closed: if the host listener isn't reachable, the workspace refuses to start. The same shape goes in `microagent.yaml`:

```yaml
mediation:
  enabled: true
  required: true
  port: 2048
  target: 127.0.0.1:9900
  failClosed: true
```

Use `--mediation-optional` only for development paths where the workspace may boot without the host-side mediator.

For the architecture and a worked pattern, see [Wire up the mediation channel](../recipes/mediation-channel.md).

## What's visible

The network record appears in JSON output from `create`, `start`, `status`, and `ps`. Backend-specific wiring (TAP names, Firecracker config paths) stays behind the supervisor protocol — you don't see or configure it. Malformed port forwards fail closed before any request is sent.
