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
- backend-specific scratch (PID files for Firecracker, console sockets for
  Apple VF)

`microagent ps` reads this directory. `microagent delete` removes a
workspace's subdirectory.

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
`stopping`, `stopped`, and `failed`. Commands such as `kill` and `delete` still
return lifecycle events, usually with state `stopped` and a `detail` field.
Callers should treat these strings as the authoritative source of truth, not
log scraping.

See the [supervisor protocol](/protocol/) for the shared request and response
schema.
