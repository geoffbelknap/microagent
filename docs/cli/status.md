---
title: microagent status
description: Show the current state of a workspace.
---

```text
microagent status <name> [--state-dir <dir>]
microagent status --name <name> [--state-dir <dir>]
```

`status` reads the state file for one workspace and prints the latest event:
identity, state (`prepared`, `running`, `halted`, `stopped`, `failed`), and
backend.

With `--json`, named workspaces also include a `verification` block. It reports
the recorded OCI image reference/digest and current SHA-256 values for the
kernel, rootfs, and injected init binary. If a current hash differs from the
recorded value, `verification.ok` is false and `verification.divergence`
contains machine-readable mismatch records.

JSON status also includes `readiness`:

- `guestReady` is true after the workspace reaches a started runtime state.
- `shellReady` is true when a running workspace has console input available.
- `resultReady` is true when the guest result file has been delivered.

When a result is ready, `status --json` includes the same structured `result`
payload returned by [`microagent result`](/cli/result/).

## Flags

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name (also accepted as positional) |
| `--state-dir <dir>` | State directory holding the workspace record |
| `--supervisor <path>` | Override the active backend supervisor path |
| `--json` | Print structured JSON output |

## Examples

```bash
microagent status --name research
microagent status agent-1 --state-dir /tmp/microagent-kit --json
```

## Related

- [`ps`](/cli/ps/) for a list view
- [State and identity](/concepts/state-and-identity/)
