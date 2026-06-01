---
title: microagent quarantine
description: Sever host-side workspace effects while preserving forensic state.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

```text
microagent quarantine <name> [--state-dir <dir>]
```

`quarantine` records the workspace state as `quarantined` and preserves disk
state, identity, runtime state files, serial logs, and `events.json`.

On Firecracker, quarantine does not signal the VM process. It terminates
host-side port-forwarding, removes transient network devices, and unlinks the
workspace vsock socket so mediation and other host-side vsock paths fail
closed for new connections.

On Apple VF, quarantine sends a control signal to the live supervisor process.
The supervisor detaches Virtualization.framework network attachments, removes
host-side vsock listeners including mediation, closes published TCP listeners,
and removes the serial input FIFO. The VM process remains alive, and the
recorded runtime PID is preserved in state.

## Flags

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name; positional name is also accepted |
| `--id <id>` | Workspace ID alias for `--name` |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`/`--supervisor`.

## Exit status

`quarantine` exits nonzero when the workspace cannot be found or when the
backend cannot sever its host-side effects. In AX mode a failure is written as a
structured error envelope.

## Example

```bash
microagent quarantine research
```

## Related

- [`status`](/cli/status/), [`halt`](/cli/halt/), [`stop`](/cli/stop/), [`kill`](/cli/kill/)
