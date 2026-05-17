---
title: State and identity
description: Where workspace state lives and how identity flows through requests.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-12_

microagent reports VM state changes as JSON events. Every request carries
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
- **`role`** — caller-supplied label. Defaults to `workload`. microagent
  records it in requests, state files, and events but does not interpret it —
  use `workload` for an agent workspace; pick another label only if you're
  starting an enforcement component or other non-workload caller in your own
  runtime.
- **`backend`** — the backend the supervisor should target.

The CLI builds the identity automatically on the high-level `run` and
`create` paths — workspaces default to `role: workload` and the runtime ID
comes from `--name` / `--id`. The lower-level `create --rootfs` path and
`--json` requests let callers set `role` explicitly; see
[`microagent create`](/cli/create/) for the flag surface.

## State directory

State lives under `--state-dir`, default `~/.microagent/`. Each workspace
gets its own subdirectory containing:

- the rootfs disk and any built bundles
- a JSON state file with the latest event
- a durable JSON event timeline
- backend-specific scratch (PID files for Firecracker, console sockets for
  Apple VF, HCS runtime IDs for Windows Hyper-V)

`microagent ps` reads this directory. `microagent delete` removes a
workspace's subdirectory.

## Runtime verification

Named workspaces persist a verification record in their manifest when the
rootfs is built or copied from the local image store. The record includes:

- OCI image reference, resolved reference, and digest when available
- kernel path and SHA-256
- rootfs path and SHA-256
- injected guest init path and SHA-256

`microagent --json status <name>` recomputes the current file hashes and
compares enforced artifacts with the recorded values. Kernel and injected-init
hashes are enforced on every status check. Rootfs hashes are enforced while the
workspace is still `prepared`; once the workspace starts, the rootfs is the
writable VM disk, so status reports current and recorded rootfs hashes without
treating normal guest writes as drift. Enforced mismatches are reported under
`verification.divergence`; callers do not need to scrape logs or reimplement
hash checks for immutable runtime artifacts.

## Readiness

Status responses include readiness signals so callers can sequence work without
polling files or serial logs:

- **`guestReady`** — the backend has concrete evidence that the guest reached
  a started runtime state. Backends do not have to treat a hypervisor process
  state as guest readiness.
- **`shellReady`** — console input is available and the configured shell has
  reached the backend's readiness gate.
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

States cover the lifecycle: `unknown`, `prepared`, `starting`, `running`,
`stopping`, `halted`, `quarantined`, `stopped`, and `failed`. `halted` means
the workspace was cleanly stopped with disk state and identity preserved for a
later `start`. `quarantined` means host-side network, mediation, and side
effect paths were severed while preserving disk state and event history.
`start` is disk-state resume from `prepared`, `halted`, `stopped`, or
`failed`; `quarantined` must be explicitly halted, stopped, or killed before it
can be started again.
Commands such as `kill` and `delete` still return lifecycle events, usually
with state `stopped` and a `detail` field. Callers should treat these strings as
the authoritative source of truth, not log scraping.

```mermaid
stateDiagram-v2
    [*] --> prepared : create

    prepared --> starting : start
    halted    --> starting : start
    stopped   --> starting : start
    failed    --> starting : start

    starting --> running

    running --> halted      : halt
    running --> stopped     : stop / kill
    running --> quarantined : quarantine
    running --> failed      : runtime error

    quarantined --> halted  : halt
    quarantined --> stopped : stop / kill

    prepared --> [*] : delete
    halted   --> [*] : delete
    stopped  --> [*] : delete
    failed   --> [*] : delete
```

Two non-obvious things to read from that diagram:

- **Nothing goes directly from `quarantined` back to `start`.** Quarantine is a forensic state — you have to halt, stop, or kill it first, then start from the resulting clean state.
- **`running` has no direct path to `delete`.** `delete` refuses while a VM process is alive (Firecracker), so you have to take the workspace through halt, stop, or kill first.

`unknown` and `stopping` are real states the API can report — `unknown` for unrecognized state files, `stopping` as the transient between `running` and a terminal state — but neither sits between user-driven transitions, so they're omitted above.

Each state write updates `<state-dir>/<runtimeID>/event.json` with the latest
event and appends the same record to `<state-dir>/<runtimeID>/events.json`.
The timeline survives VM runtime exit and is intentionally small: it is a
forensic lifecycle record, not a log stream.

See the [supervisor protocol](/protocol/) for the shared request and response
schema.
