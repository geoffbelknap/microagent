---
title: Runtime contract
description: Depend on one set of runtime semantics across Firecracker, Apple VF, and Hyper-V.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-13_

If you are building an agent runtime on top of microagent, this page defines
the semantics you can rely on across every backend - Firecracker, Apple VF,
and Windows Hyper-V. `microagent --json contract` is the JSON
source for the shared runtime contract.

## Scope

Backends expose the same public runtime primitives. A backend may advertise a
smaller command surface for commands it does not yet implement while preserving
the same state, result, readiness, and diagnostic field shapes for the commands
it supports:

| Primitive | Contract |
|---|---|
| Lifecycle | `prepare`, `start`, `run`, `inspect`, `halt`, `quarantine`, `pause`, `resume`, `snapshot`, `stop`, `kill`, `delete` |
| States | `unknown` (zero/unobserved state), `prepared`, `starting`, `running`, `paused`, `halted`, `quarantined`, `stopped`, `failed` |
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

## Pause/resume and snapshots (capability-gated)

`pause`, `resume`, and `snapshot` are memory-state primitives, distinct from
the disk-state `halt`/`start`:

- `pause` freezes a running workspace's vCPUs while preserving memory and disk
  (state `paused`, `runtimeMayContinue: true`); `resume` thaws it back to
  `running`. `exec`, `connect`, and `stats` are rejected while paused.
- `snapshot` captures a memory-plus-disk checkpoint. `start --from-snapshot`
  restores it in place (rollback); `create --from-snapshot` forks a new
  workspace from it.

These are capability-gated, not universal. A backend advertises support through
`microagent --json host`: `pauseResumeAvailable` and `snapshotAvailable` are
`true` on Firecracker and absent/false on Apple VF and Windows Hyper-V, which
return a structured unsupported error for these commands (the same pattern as
console input). The `paused` state and the commands stay in the backend-neutral
contract so clients share one vocabulary; availability is per host.

**Connection-reset contract:** restoring or forking a snapshot re-establishes
host networking fresh, so in-flight guest connections - outbound TCP and live
vsock sessions (exec/shell/mediation) - do not survive. The guest process must
reconnect. Bridged networking is unsupported for snapshot/fork.

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
