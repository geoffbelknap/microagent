---
title: microagent status
description: Show the current state of a workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-17_

```text
microagent [--json] status <name> [--state-dir <dir>]
microagent [--json] status --name <name> [--state-dir <dir>]
microagent inspect <name> [--state-dir <dir>]
```

`status` reads the state file for one workspace and prints the latest event:
identity, state (`prepared`, `running`, `halted`, `quarantined`, `stopped`, `failed`), and
backend.

`inspect` is a familiar alias for `status` that defaults to structured JSON
output.

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
- `resultReady` is true when the guest result file has been delivered.

JSON status includes declared network intent under `network`. When a backend
records runtime assignment details, `network.runtime` contains the latest guest
IP, subnet, gateway, DNS, and routes.

When a result is ready, `microagent --json status` includes the same structured `result`
payload returned by [`microagent result`](/cli/result/).

Named workspaces also include `artifacts` when inputs or outputs were declared.
`artifacts.ingress` lists attached bundle inputs, and `artifacts.egress` lists
declared output paths. Use [`artifacts get`](/cli/artifacts/) to retrieve a
declared output by name without entering the workspace.

## Flags

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name (also accepted as positional) |
| `--id <id>` | Workspace ID alias for `--name` |
| `--state-dir <dir>` | State directory holding the workspace record |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--json` | Global flag before `status`; print structured JSON output |

## Examples

```bash
microagent status --name research
microagent --json status agent-1 --state-dir /tmp/microagent
microagent inspect research
```

## Related

- [`ps`](/cli/ps/) for a list view
- [State and identity](/concepts/state-and-identity/)
