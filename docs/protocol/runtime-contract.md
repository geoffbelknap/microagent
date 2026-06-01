---
title: Runtime contract
description: Backend-neutral agent runtime semantics shared by microagent backends.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

`microagent --json contract` is the JSON source for the shared runtime
contract. Agent-runtime builders can depend on one set of semantics across
Firecracker, Apple VF, and experimental Windows Hyper-V support.

## Scope

Stable backends expose the same public runtime primitives. Experimental
backends may advertise a smaller command surface while preserving the same
state, result, readiness, and diagnostic field shapes for supported commands:

| Primitive | Contract |
|---|---|
| Lifecycle | `prepare`, `start`, `run`, `inspect`, `halt`, `quarantine`, `stop`, `kill`, `delete` |
| States | `unknown` (zero/unobserved state), `prepared`, `starting`, `running`, `halted`, `quarantined`, `stopped`, `failed` |
| Readiness | `guestReady`, `shellReady`, `execReady`, `resultReady`, `mediationReady` |
| Result | `identity`, `backend`, `resultPath`, `startedAt`, `completedAt`, `exitCode`, `stdout`, `stderr`, `error` |
| Artifacts | `ingress`, `egress`; declared egress artifacts are retrievable by name without entering the workspace |
| Mediation | `enabled`, `required`, `port`, `target`, `failClosed` |
| Verification | image digest, kernel hash, rootfs hash, init hash, divergence entries |

`mediationReady` means the declared mediation target is live reachable for a
running workspace. Optional mediation target failures report `ready: false`
without a hard `error`; required mediation target failures report `ready: false`
with an error.

## Backend rules

Backend-specific mechanics stay behind supervisor boundaries. Firecracker may
use PIDs, TAP devices, Unix sockets, and process groups; Apple VF may use
Virtualization.framework process state; Windows Hyper-V may use HCS compute
systems and VHD root disks. Public output stays backend-neutral: structured
requests, responses, state events, readiness, results, artifact declarations,
mediation declaration, and verification.

`start` is disk-state resume. It may boot from `prepared`, `halted`,
`stopped`, or `failed`; it must reject `starting` and `running`.

`halt` is a clean disk-preserving shutdown. It is not memory pause/resume.

`quarantine` preserves disk state and event history while severing host-side
network, mediation, and side-effect paths. Firecracker can preserve the VM
process PID while severing those host-side paths; another backend may use
different mechanics, but the public state remains `quarantined`.
Consumers must not treat `quarantined` as a normal stopped state. The workspace
must be halted, stopped, or killed before `start` boots it again from disk.

## Contract command

```bash
microagent --json contract
```

The output is versioned as `agent-runtime.v1`. Consumers should use the JSON
fields instead of scraping documentation prose.

## Related

- [Supervisor protocol](/protocol/)
- [Firecracker supervisor](/protocol/firecracker/)
- [Apple VF supervisor](/protocol/applevf/)
- [Windows Hyper-V supervisor](/protocol/windows-hyperv/)
