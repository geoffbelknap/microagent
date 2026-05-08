---
title: microagent status
description: Show the current state of a workspace.
---

```text
microagent [--json] status <name> [--state-dir <dir>]
microagent [--json] status --name <name> [--state-dir <dir>]
```

`status` reads the state file for one workspace and prints the latest event:
identity, state (`prepared`, `running`, `halted`, `quarantined`, `stopped`, `failed`), and
backend.

With the global `--json` flag, named workspaces also include a `verification` block. It reports
the recorded OCI image reference/digest and current SHA-256 values for the
kernel, rootfs, and injected init binary. If a current hash differs from the
recorded value, `verification.ok` is false and `verification.divergence`
contains machine-readable mismatch records.

JSON status also includes `readiness`:

- `guestReady` is true after the workspace reaches a started runtime state.
- `shellReady` is true when a running workspace has console input available.
- `resultReady` is true when the guest result file has been delivered.

JSON status includes declared network intent under `network`. When a backend
records runtime assignment details, `network.runtime` contains the latest guest
IP, subnet, gateway, DNS, and routes.

When a result is ready, `microagent --json status` includes the same structured `result`
payload returned by [`microagent result`](result.md).

Named workspaces also include `artifacts` when inputs or outputs were declared.
`artifacts.ingress` lists attached bundle inputs, and `artifacts.egress` lists
declared output paths. Use [`artifacts get`](artifacts.md) to retrieve a
declared output by name without entering the workspace.

## Flags

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name (also accepted as positional) |
| `--state-dir <dir>` | State directory holding the workspace record |
| `--supervisor <path>` | Override the active backend supervisor path |
| `--json` | Global flag before `status`; print structured JSON output |

## Examples

```bash
microagent status --name research
microagent --json status agent-1 --state-dir /tmp/microagent-kit
```

## Related

- [`ps`](ps.md) for a list view
- [State and identity](../concepts/state-and-identity.md)
