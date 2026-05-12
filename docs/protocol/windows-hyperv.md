---
title: Windows Hyper-V supervisor
description: Experimental Windows host backend for Linux guests through HCS.
---

The `windows-hyperv` backend is the experimental Windows host backend for
running Linux guests without WSL and without QEMU. It talks to Windows Host
Compute Service (HCS) through `vmcompute.dll` and prepares Hyper-V utility
VM-style compute systems from Microagent runtime requests.

For the shared command list and response shape, see
[Supervisor protocol](/protocol/). This page covers the Windows host behavior
and current limitations.

## Host Requirements

`windows-hyperv` requires:

- Windows with Host Compute Service available
- Hyper-V / Windows Hypervisor Platform support enabled
- a user token that can access HCS, typically Administrator or membership in
  the Hyper-V Administrators group
- a Linux kernel artifact for `windows-hyperv/<arch>`
- a `microagent-guestinit-<arch>` guest init binary
- a VHD root disk at the workspace rootfs path

Use:

```powershell
microagent doctor --backend windows-hyperv
```

The doctor check reports HCS availability, virtualization support, HCS access
errors, HCN/HNS networking availability, Hyper-V socket availability, kernel
support, guest-init availability, and console capability.

## Storage

Windows-HyperV consumes a VHD root disk because HCS VM configuration is
VHD-oriented. Workspace root disks live under:

```text
<state-dir>/workspaces/<runtimeID>/rootfs.vhd
```

The source contents still come from Microagent's OCI/rootfs flow. The Windows
rootfs builder converts those contents into a fixed VHD with an ext4 payload.

## Lifecycle

The current lifecycle surface is:

| Command | Status |
|---|---|
| `host` | supported |
| `check` | supported |
| `run` | supported experimentally |
| `inspect` | supported |
| `start` | supported experimentally |
| `halt` | supported experimentally |
| `quarantine` | supported experimentally |
| `stop` | supported |
| `kill` | supported |
| `delete` | supported |
| `prepare` | unsupported |
| `console` | unsupported |

Unsupported commands fail closed with structured `ok: false` responses.

`run` creates an HCS compute system, waits for guest result delivery, records
backend-neutral runtime state, and returns a stopped event with `result` when
the guest exits successfully. `start` creates a detached HCS compute system and
records enough HCS identity in `runtime.json` for later `inspect`, `connect`,
`halt`, `quarantine`, `stop`, `kill`, and `delete`.

## State

The supervisor writes backend runtime files under:

```text
<state-dir>/<runtimeID>/
```

Important files include:

| File | Purpose |
|---|---|
| `event.json` | latest lifecycle event |
| `events.json` | append-only lifecycle history |
| `runtime.json` | latest lifecycle state and HCS compute system ID |
| `serial.in` | console input compatibility marker for running workspaces |
| `serial.log` | guest serial output captured from the HCS COM1 named pipe |
| `result.json` | structured guest result when delivered |
| `hvsock-listener.log` | detached Hyper-V socket listener helper log |

`inspect` returns the latest event and readiness state. If `result.json`
exists, `inspect` also returns the backend-neutral `result` object and marks
`readiness.resultReady.ready` true.

## Current Limitations

- No WSL dependency is used or required.
- QEMU/WHPX is not used.
- `microagent connect` and `connect --send` use Hyper-V sockets.
- Published TCP networking is not available yet.
- Mediation and guest-to-host TCP listener targets use Hyper-V socket listener
  helpers.
- `prepare` is not implemented yet.
- Direct supervisor `console` is not implemented; use `microagent connect`.
- Foreground `run` supports the configured result listener by mapping the guest
  AF_VSOCK result port to a Hyper-V socket service and writing the received
  payload to `result.json`.
- Result runs configure COM1 as an HCS named pipe and append guest serial output
  to `serial.log`.

Treat this backend as experimental. It is intended for Windows Hyper-V Linux
guest support without WSL, and it should fail closed when a host prerequisite or
unsupported feature is missing.
