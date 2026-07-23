---
title: microagent halt
description: Shut a workspace down cleanly so you can start it again later.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-23_

```text
microagent halt <name> [--state-dir <dir>]
```

`halt` is the one graceful-shutdown verb and the normal way to park a workspace:
it requests a clean shutdown and records the terminal state as `halted`. The VM
process exits, but the rootfs, attached disks, identity, and event timeline
remain under `--state-dir`, so a later `microagent start <name>` boots the same
disk state. `stop` is an alias of `halt` and behaves identically.

The guest gets a fixed graceful window (about five seconds) to exit. If it does
not exit in time, the workspace is recorded as `failed` and `halt` returns an error
without escalating—follow up with [`kill`](/cli/kill/) for a hard termination. For containment
without a shutdown, see [`quarantine`](/cli/quarantine/).

This is not memory pause/resume - a halted workspace boots again from the
preserved disk. For memory-state suspend, see [`pause`](/cli/pause/).

## Examples

Park a workspace, then pick it back up later:

```bash
microagent halt research
microagent start research
```

If the guest does not exit within the graceful window, force it:

```bash
microagent kill research
```

## Flags

You'll rarely need flags here - `--state-dir` only when the workspace lives
outside the default `~/.microagent/`.

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name; positional name is also accepted |
| `--id <id>` | Workspace ID alias for `--name` |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--mode`/`--supervisor`.

## Exit status

`halt` exits `0` on success; nonzero when the workspace cannot be found, the
clean shutdown fails, or the guest does not exit within the graceful window. In
AX mode a failure is written as a structured error envelope.

## Related

- [`start`](/cli/start/) - boot the halted workspace again
- [`kill`](/cli/kill/) - force-terminate when the guest does not exit
- [`quarantine`](/cli/quarantine/) - contain a workspace without shutting it down
- [`status`](/cli/status/) - confirm the `halted` state
