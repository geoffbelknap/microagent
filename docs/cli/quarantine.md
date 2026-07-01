---
title: microagent quarantine
description: Sever host-side workspace effects while preserving forensic state.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-27_

```text
microagent quarantine <name> [--state-dir <dir>]
```

`quarantine` severs a workspace's host-side network and mediation while
preserving disk state, identity, runtime state files, serial logs, and
`events.json`, and records the state as `quarantined`. It is the containment
verb, not a shutdown: [`halt`](/cli/halt/) parks a healthy workspace and
[`stop`](/cli/stop/) signals the VM to exit, but `quarantine` leaves the VM
process where it is and cuts its ability to affect anything outside the
boundary. A quarantined workspace is a forensic state - you must halt, stop,
or kill it before you can `start` it again.

Quarantine removes host-side network paths, mediation listeners, published TCP
listeners, and console input where they exist. New connections fail closed. The
VM process may still be alive, and the recorded runtime PID is preserved in
state so you can inspect what happened before taking it down.

## Examples

Quarantine a workspace, inspect it, then take it down:

```bash
microagent quarantine research
microagent status research
microagent kill research
```

## Flags

You'll rarely need flags here - `--state-dir` only when the workspace lives
outside the default `~/.microagent/`.

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name; positional name is also accepted |
| `--id <id>` | Workspace ID alias for `--name` |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`/`--supervisor`.

## Exit status

`quarantine` exits `0` when the host-side effects are severed and the state is
recorded; nonzero when the workspace cannot be found or the backend cannot
sever its host-side effects. In AX mode a failure is written as a structured
error envelope.

## Related

- [`halt`](/cli/halt/) - park a healthy workspace instead
- [`stop`](/cli/stop/) - signal the VM to shut down
- [`kill`](/cli/kill/) - force-terminate a quarantined VM
- [`status`](/cli/status/) - confirm the `quarantined` state
- [State and identity](/concepts/state-and-identity/) - where `quarantined` sits in the lifecycle
