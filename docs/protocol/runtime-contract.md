---
title: Runtime contract
description: Backend-neutral agent runtime semantics shared by Firecracker and Apple VF.
---

`microagent --json contract` is the JSON source for the shared runtime
contract. Agent-runtime builders can depend on one set of semantics across
Firecracker and Apple VF.

## Scope

Both backends expose the same public runtime primitives:

| Primitive | Contract |
|---|---|
| Lifecycle | `prepare`, `start`, `run`, `inspect`, `halt`, `quarantine`, `stop`, `kill`, `delete` |
| States | `prepared`, `starting`, `running`, `halted`, `quarantined`, `stopped`, `failed` |
| Readiness | `guestReady`, `shellReady`, `resultReady`, `mediationReady` |
| Result | `identity`, `backend`, `resultPath`, `startedAt`, `completedAt`, `exitCode`, `stdout`, `stderr`, `error` |
| Artifacts | `ingress`, `egress`; declared egress artifacts are retrievable by name without entering the workspace |
| Mediation | `enabled`, `required`, `port`, `target`, `failClosed` |
| Verification | image digest, kernel hash, rootfs hash, init hash, divergence entries |

## Backend rules

Backend-specific mechanics stay behind supervisor boundaries. Firecracker may use PIDs, TAP devices, Unix sockets, and process groups; Apple VF may use Virtualization.framework process state. Public output stays the same across both: structured requests, responses, state events, readiness, results, artifact declarations, mediation declaration, and verification.

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

- [Supervisor protocol](index.md)
- [Firecracker supervisor](firecracker.md)
- [Apple VF supervisor](applevf.md)
