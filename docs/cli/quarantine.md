---
title: microagent quarantine
description: Sever host-side workspace effects while preserving forensic state.
---

```text
microagent quarantine <name> [--state-dir <dir>]
```

`quarantine` records the workspace state as `quarantined` and preserves disk
state, identity, runtime state files, serial logs, and `events.json`.

On Firecracker, quarantine does not signal the VM process. It terminates
host-side port-forwarding, removes transient network devices, and unlinks the
workspace vsock socket so mediation and other host-side vsock paths fail
closed for new connections.

## Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory |
| `--backend <name>` | Backend override |
| `--supervisor <path>` | Override the active backend supervisor path |

## Example

```bash
microagent quarantine research
```

## Related

- [`status`](/cli/status/), [`halt`](/cli/halt/), [`stop`](/cli/stop/), [`kill`](/cli/kill/)
