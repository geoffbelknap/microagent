---
title: microagent status
description: Show one workspace's state, readiness, and verification detail.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-14_

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
  "backend": "firecracker",
  "event": {
    "identity": { "runtimeID": "research", "role": "workload", "backend": "firecracker" },
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
  "network": {
    "mode": "nat",
    "portForwards": [
      { "protocol": "tcp", "host": "127.0.0.1", "hostPort": 8080, "guestPort": 80 }
    ],
    "runtime": {
      "mode": "nat",
      "ip": "10.43.12.2/29",
      "gateway": "10.43.12.1",
      "dns": ["1.1.1.1"]
    }
  }
}
```

## Flags

You'll rarely need flags here - the global `--json` before the subcommand is
the one that matters, since it unlocks the readiness, verification, network,
and result blocks below.

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name (also accepted as positional) |
| `--id <id>` | Workspace ID alias for `--name` |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--json` | Global flag before `status`; print structured JSON output |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`/`--supervisor`.

## What JSON status includes

With the global `--json` flag, named workspaces also include a `verification` block. It reports
the recorded OCI image reference/digest and current SHA-256 values for the
kernel, rootfs, and injected init binary. If a current hash differs from the
recorded value, `verification.ok` is false and `verification.divergence`
contains machine-readable mismatch records.

JSON status also includes `readiness`:

- `guestReady` is true when the backend has concrete evidence that the guest
  reached a started runtime state.
- `shellReady` is true when console input is available and the configured shell
  has reached the backend's readiness gate.
- `execReady` is true when the structured exec service accepts a no-op exec
  request and returns a successful structured result.
- `resultReady` is true when the guest result file has been delivered.
- `mediationReady` is true when configured mediation is enabled on a running
  workspace and the declared host target accepts a bounded TCP probe. Optional
  mediation target failures leave the signal not ready without a hard error;
  required mediation target failures include an error.

JSON status includes declared network intent under `network`. When a backend
records runtime assignment details, `network.runtime` contains the latest guest
IP, subnet, gateway, DNS, and routes.

When a result is ready, `microagent --json status` includes the same structured `result`
payload returned by [`microagent result`](/cli/result/).

Named workspaces also include `artifacts` when inputs or outputs were declared.
`artifacts.ingress` lists attached bundle inputs, and `artifacts.egress` lists
declared output paths. Use [`artifact get`](/cli/artifact/) to retrieve a
declared output by name without entering the workspace.

## Exit status

`status` exits `0` when the workspace record is found and read; nonzero when
the workspace cannot be found or its state file cannot be read. In AX mode a
missing workspace is written as a structured `not_found` error envelope.

## Related

- [`list`](/cli/list/) - the list view across all workspaces
- [State and identity](/concepts/state-and-identity/) - the state model behind these fields
- [Readiness semantics](/concepts/state-and-identity/#readiness) - what each readiness signal means
