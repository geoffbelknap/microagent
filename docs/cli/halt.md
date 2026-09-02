---
title: microagent halt
description: Shut a workspace down cleanly so you can start it again later.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-15_

```text
microagent halt <name> [--reason <text>] [--state-dir <dir>]
```

`halt` is the one graceful-shutdown verb and the normal way to park a workspace:
it requests a clean shutdown and records the terminal state as `halted`. The VM
process exits, but the rootfs, attached disks, identity, and event timeline
remain under `--state-dir`, so a later `microagent start <name>` boots the same
disk state. `stop` is an alias of `halt` and behaves identically.

Human output reports work-in-flight capture, filesystem synchronization, the
guest shutdown request, and the backend stop transition on stderr. Fast halts
may finish before the delayed progress indicator appears. JSON and MCP results
remain unchanged.

Before shutdown, microagent asks the guest's structured exec service to run a
filesystem `sync`. The request is bounded to two seconds, so an unavailable or
uncooperative guest cannot delay its own halt indefinitely. The lifecycle event
history records whether the flush completed. If it fails or times out, shutdown
still proceeds and preserves only data the guest had already flushed.

Microagent then sends a narrow shutdown request to guest PID 1. PID 1 forwards the OCI
image's `StopSignal` to the workload process group (`SIGTERM` when the image
does not declare one), waits up to ten seconds for it to exit, and then powers
off the guest. The host gives that sequence a fixed window of about 15 seconds.
If the VM does not exit in time, the workspace is recorded as `failed` and
`halt` returns an error without terminating the VMM—follow up with
[`kill`](kill.md) for a hard termination. For containment, see
[`quarantine`](quarantine.md).

A workspace that is already running with an older `microagent-init` may reject
the shutdown control request. `halt` fails closed in that case instead of
silently terminating the VMM. Use [`kill`](kill.md) for that running
instance, then recreate the workspace with the current guest init before
relying on graceful halt.

Use [`kill`](kill.md) when you explicitly want immediate termination without
the flush or shutdown sequence.

This is not memory pause/resume - a halted workspace boots again from the
preserved disk. For memory-state suspend, see [`pause`](pause.md).

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

See [global flags](index.md#global-flags) for `--output`/`--json`/`--supervisor`.

## Exit status

`halt` exits `0` on success; nonzero when the workspace cannot be found, the
clean shutdown fails, or the guest does not exit within the graceful window.

## Related

- [`start`](start.md) - boot the halted workspace again
- [`kill`](kill.md) - force-terminate when the guest does not exit
- [`quarantine`](quarantine.md) - freeze, sever, capture, and stop a workspace into custody
- [`status`](status.md) - confirm the `halted` state
