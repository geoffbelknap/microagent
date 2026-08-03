---
title: microagent halt
description: Shut a workspace down cleanly so you can start it again later.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-03_

```text
microagent halt <name> [--reason <text>] [--state-dir <dir>]
```

`halt` is the one graceful-shutdown verb and the normal way to park a workspace:
it requests a clean shutdown and records the terminal state as `halted`. The VM
process exits, but the rootfs, attached disks, identity, and event timeline
remain under `--state-dir`, so a later `microagent start <name>` boots the same
disk state. `stop` is an alias of `halt` and behaves identically.

Before shutdown, microagent asks the guest's structured exec service to run a
filesystem `sync`. The request is bounded to two seconds, so an unavailable or
uncooperative guest cannot delay its own halt indefinitely. The lifecycle event
history records whether the flush completed. If it fails or times out, halt
still proceeds and preserves only data the guest had already flushed. Use
[`kill`](/cli/kill/) when you explicitly want immediate termination without a
flush attempt.

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

`--state-dir` matters only when the workspace lives outside the default
`~/.microagent/`.

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name; positional name is also accepted |
| `--id <id>` | Workspace ID alias for `--name` |
| `--reason <text>` | Opaque reason recorded as the lifecycle event's `purpose` |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--supervisor`.

## Exit status

`halt` exits `0` on success; nonzero when the workspace cannot be found, the
clean shutdown fails, or the guest does not exit within the graceful window.

## Related

- [`start`](/cli/start/) - boot the halted workspace again
- [`kill`](/cli/kill/) - force-terminate when the guest does not exit
- [`quarantine`](/cli/quarantine/) - contain a workspace without shutting it down
- [`status`](/cli/status/) - confirm the `halted` state
