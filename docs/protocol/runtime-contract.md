---
title: Runtime parity contract
description: Backend-neutral agent runtime semantics shared by Firecracker and Apple VF.
---

`microagent contract --json` is the machine-readable source for the shared
runtime contract. It exists so agent-runtime builders can depend on one set of
semantics across Firecracker and Apple VF.

## Scope

Both backends expose the same public runtime primitives:

| Primitive | Contract |
|---|---|
| Lifecycle | `prepare`, `start`, `run`, `inspect`, `halt`, `quarantine`, `stop`, `kill`, `delete` |
| States | `prepared`, `starting`, `running`, `halted`, `quarantined`, `stopped`, `failed` |
| Readiness | `guestReady`, `shellReady`, `resultReady`, `mediationReady` |
| Result | `identity`, `backend`, `resultPath`, `startedAt`, `completedAt`, `exitCode`, `stdout`, `stderr`, `error` |
| Artifacts | `ingress`, `egress` |
| Mediation | `enabled`, `required`, `port`, `target`, `failClosed` |
| Verification | image digest, kernel hash, rootfs hash, init hash, divergence entries |

## Backend Rules

Backend-specific mechanics stay behind supervisor boundaries. Firecracker may
use PIDs, TAP devices, Unix sockets, and process groups; Apple VF may use
Virtualization.framework process state. Public output remains the same:
structured requests, responses, state events, readiness, results, artifact
declarations, mediation declaration, and verification.

`halt` is a clean disk-preserving shutdown. It is not memory pause/resume.

`quarantine` preserves disk state and event history while severing host-side
network, mediation, and side-effect paths. Firecracker can preserve the VM
process PID while severing those host-side paths; another backend may use
different mechanics, but the public state remains `quarantined`.

## Contract Command

```bash
microagent contract --json
```

The output is versioned as `agent-runtime.v1`. Consumers should use the JSON
fields instead of scraping documentation prose.

## Related

- [Supervisor protocol](/protocol/)
- [Firecracker supervisor](/protocol/firecracker/)
- [Apple VF supervisor](/protocol/applevf/)
