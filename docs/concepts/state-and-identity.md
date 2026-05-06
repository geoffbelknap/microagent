---
title: State and identity
description: Where workspace state lives and how identity flows through requests.
---

Microagent treats VM state changes as structured output. Every request carries
an identity block; every response carries an event block describing the
resulting state.

## Identity

Every request has an identity:

```json
{
  "identity": {
    "requestID": "req-1",
    "runtimeID": "agent-1",
    "role": "workload",
    "backend": "apple-vf"
  }
}
```

- **`requestID`** — unique for this call. Echoed in the event so callers can
  correlate.
- **`runtimeID`** — the workspace identifier. Equivalent to `--name` /
  `--id`.
- **`role`** — caller-supplied label. Microagent does not interpret it.
- **`backend`** — the backend the supervisor should target.

The CLI builds the identity automatically. Callers using `--json` requests
supply it directly.

## State directory

State lives under `--state-dir`, default `~/.microagent/`. Each workspace
gets its own subdirectory containing:

- the rootfs disk and any built bundles
- a JSON state file with the latest event
- a durable JSON event timeline
- backend-specific scratch (PID files for Firecracker, console sockets for
  Apple VF)

`microagent ps` reads this directory. `microagent delete` removes a
workspace's subdirectory.

## Runtime verification

Named workspaces persist a verification record in their manifest when the
rootfs is built or copied from the local image store. The record includes:

- OCI image reference, resolved reference, and digest when available
- kernel path and SHA-256
- rootfs path and SHA-256
- injected guest init path and SHA-256

`microagent status --json` recomputes the current file hashes and compares them
with the recorded values. A mismatch is reported under
`verification.divergence`; callers do not need to scrape logs or reimplement
hash checks to detect runtime drift.

## Readiness

Status responses include a readiness block for consumers that need to sequence
work without polling files or serial logs:

- **`guestReady`** — the workspace reached a started runtime state.
- **`shellReady`** — the workspace is running and console input is available.
- **`resultReady`** — the guest result file exists.
- **`mediationReady`** — a declared mediation channel is ready for a running
  workspace.

Each signal carries `ready`, optional `observedAt`, and optional detail/error
fields.

## Events

Lifecycle responses include an event:

```json
{
  "ok": true,
  "backend": "apple-vf",
  "event": {
    "identity": { "...": "..." },
    "state": "prepared",
    "observedAt": "2026-05-02T00:00:00Z"
  }
}
```

States cover the full lifecycle: `unknown`, `prepared`, `starting`, `running`,
`stopping`, `halted`, `quarantined`, `stopped`, and `failed`. `halted` means
the workspace was cleanly stopped with disk state and identity preserved for a
later `start`. `quarantined` means host-side network, mediation, and side
effect paths were severed while preserving disk state and event history.
Commands such as `kill` and `delete` still return lifecycle events, usually
with state `stopped` and a `detail` field. Callers should treat these strings as
the authoritative source of truth, not log scraping.

Each state write updates `<state-dir>/<runtimeID>/event.json` with the latest
event and appends the same record to `<state-dir>/<runtimeID>/events.json`.
The timeline survives VM runtime exit and is intentionally small: it is a
forensic lifecycle record, not a log stream.

See the [supervisor protocol](/protocol/) for the shared request and response
schema.
