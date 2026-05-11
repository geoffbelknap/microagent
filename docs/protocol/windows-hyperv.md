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
errors, kernel support, guest-init availability, and the current lack of
interactive console support.

## Storage

Windows-HyperV consumes a VHD root disk because HCS VM configuration is
VHD-oriented. Workspace root disks live under:

```text
<state-dir>/workspaces/<runtimeID>/rootfs.vhd
```

The source contents still come from Microagent's OCI/rootfs flow. The Windows
rootfs builder converts those contents into a fixed VHD with an ext4 payload.

## Lifecycle

The V1 lifecycle surface is intentionally narrow:

| Command | Status |
|---|---|
| `host` | supported |
| `check` | supported |
| `run` | supported experimentally |
| `inspect` | supported |
| `stop` | supported |
| `kill` | supported |
| `delete` | supported |
| `prepare` | unsupported |
| `start` | unsupported |
| `console` | unsupported |
| `halt` | unsupported |
| `quarantine` | unsupported |

Unsupported commands fail closed with structured `ok: false` responses.

`run` creates an HCS compute system, waits for pending HCS create/start
completion notifications, records backend-neutral runtime state, and returns a
running event. `stop`, `kill`, and `delete` use the recorded compute system ID
from `runtime.json`.

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
| `serial.log` | serial log path reserved for guest output |
| `result.json` | structured guest result when delivered |

`inspect` returns the latest event and readiness state. If `result.json`
exists, `inspect` also returns the backend-neutral `result` object and marks
`readiness.resultReady.ready` true.

## Current Limitations

- No WSL dependency is used or required.
- QEMU/WHPX is not used.
- Interactive `microagent connect` is not available yet.
- Published TCP networking is not available yet.
- Mediation and arbitrary guest-to-host listener parity are not available yet.
- `prepare` / `start`, `halt`, and `quarantine` are not implemented yet.
- The result and serial file semantics are present in state responses, but the
  richer host/guest transport that writes them is still experimental.

Treat this backend as an implementation target for Windows Hyper-V Linux guest
support, not as full Firecracker or Apple VF parity yet.
