---
title: microagent status
description: Show one workspace's state, readiness, and verification detail.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-03_

```text
microagent [--json] status <name> [--state-dir <dir>]
microagent [--json] status --name <name> [--state-dir <dir>]
```

`status` reads the state file for one workspace and prints the latest event:
identity, state (`prepared`, `running`, `halted`, `quarantined`, `stopped`, `failed`), and
backend. It's the single-workspace deep view; use [`list`](/cli/list/) when you
want one row per workspace across the whole state directory.

## Examples

Check a workspace:

```bash
microagent status research
```

Get the full structured view, or point at another state directory:

```bash
microagent --json status agent-1 --state-dir /tmp/microagent
```

A trimmed `microagent --json status` response for a running workspace, showing
the readiness, verification, and network blocks:

```json
{
  "ok": true,
  "backend": "linux-kvm",
  "event": {
    "identity": { "runtimeID": "research", "role": "workload", "backend": "linux-kvm" },
    "state": "running",
    "observedAt": "2026-06-01T12:00:00Z"
  },
  "verification": {
    "ok": true,
    "imageRef": "docker.io/library/ubuntu:24.04",
    "imageDigest": "sha256:...",
    "rootfs": { "sha256": "...", "recordedSHA256": "..." }
  },
  "readiness": {
    "guestReady": { "ready": true },
    "shellReady": { "ready": true },
    "execReady": { "ready": true },
    "resultReady": { "ready": false },
    "mediationReady": { "ready": false }
  },
  "egressCapture": {
    "mode": "broker",
    "provider": "linux-netfilter-prerouting",
    "coverageStatus": "complete",
    "encryptedDNS": "not-observable",
    "live": true,
    "livenessDetail": "egress mediator process 1234 is running"
  },
  "network": {
    "mode": "user",
    "portForwards": [
      { "protocol": "tcp", "host": "127.0.0.1", "hostPort": 8080, "guestPort": 80 }
    ],
    "runtime": {
      "mode": "user",
      "ip": "10.43.12.2/29",
      "gateway": "10.43.12.1",
      "dns": ["1.1.1.1"]
    }
  }
}
```

## Flags

The global `--json` before the subcommand is the flag that matters here:
it unlocks the readiness, verification, network, and result blocks below.

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name (also accepted as positional) |
| `--id <id>` | Workspace ID alias for `--name` |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--json` | Global flag before `status`; print structured JSON output |

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--supervisor`.

## What JSON status includes

With the global `--json` flag, named workspaces also include a `verification` block. It reports
the recorded OCI image reference/digest and current SHA-256 values for the
kernel, rootfs, injected init binary, and per-boot config disk. If a current hash differs from the
recorded value, `verification.ok` is false and `verification.divergence`
contains machine-readable mismatch records.

Rootfs comparison is enforced whenever the workspace disk is quiescent:
`prepared`, `halted`, `stopped`, `quarantined`, or `failed`. While a workspace
is running, status still measures the current writable disk but does not treat
normal guest writes as divergence.

JSON status also includes `readiness`:

- `guestReady` is true when the backend has concrete evidence that the guest
  reached a started runtime state.
- `shellReady` is true when console input is available and the configured shell
  completes a bounded command round trip. The probe sends `exit`; a raw TCP
  connection is not treated as readiness because accepting a shell connection
  starts a session.
- `execReady` is true when the structured exec service accepts a no-op exec
  request and returns a successful structured result.
- `resultReady` is true when the guest result file has been delivered.
- `mediationReady` is true when configured mediation is enabled on a running
  workspace and the declared host target accepts a bounded TCP probe. Optional
  mediation target failures leave the signal not ready without a hard error;
  required mediation target failures include an error.

Periodic reconciliation records only passive readiness evidence. It does not
open shell, exec, mediation, or VMM API connections for a healthy workspace.
Live probes run only for an explicit status, inspect, or GC request.

JSON status includes declared network intent under `network`. When a backend
records runtime assignment details, `network.runtime` contains the latest guest
IP, subnet, gateway, DNS, and routes.

`egressCapture` separates declared coverage from observed enforcement
liveness. When the backend records an independently observable mediator,
`live` is `true` or `false` and `livenessDetail` identifies the observation.
If liveness cannot be observed, `live` is omitted; `coverageStatus` alone is
never a liveness claim. Observing a dead mediator also appends a persistent
enforcement-failure entry to the workspace event history.

`encryptedDNS` reports content-level coverage separately from transport
capture. It is `not-observable` in `broker` mode because allowed TLS remains
opaque, and `http1-detected-and-denied` in `mitm` mode. A locked allowlist can
still bound which destinations an opaque connection reaches; it does not make
the encrypted request content observable.

When a result is ready, `microagent --json status` includes the same structured `result`
payload returned by [`microagent result`](/cli/result/).

Named workspaces also include `artifacts` when inputs or outputs were declared.
`artifacts.ingress` lists attached bundle inputs, and `artifacts.egress` lists
declared output paths. Use [`artifact get`](/cli/artifact/) to retrieve a
declared output by name without entering the workspace.

## Exit status

`status` exits `0` when the workspace record is found and read; nonzero when
the workspace cannot be found or its state file cannot be read.

## Related

- [`list`](/cli/list/) - the list view across all workspaces
- [State and identity](/concepts/state-and-identity/) - the state model behind these fields
- [Readiness semantics](/concepts/state-and-identity/#readiness) - what each readiness signal means
