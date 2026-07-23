---
title: microagent halt
description: Shut a workspace down cleanly so you can start it again later.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-23_

```text
microagent halt <name> [--state-dir <dir>]
```

`halt` is the normal way to park a workspace: it requests a clean shutdown and
records the terminal state as `halted`. The VM process exits, but the rootfs,
attached disks, identity, and event timeline remain under `--state-dir`, so a
later `microagent start <name>` boots the same disk state. Reach for
[`stop`](/cli/stop/) when you want to signal a misbehaving VM rather than park
a healthy one, and [`kill`](/cli/kill/) only when `stop` doesn't return.

This is not memory pause/resume - a halted workspace boots again from the
preserved disk. For memory-state suspend, see [`pause`](/cli/pause/).

## Examples

Park a workspace, then pick it back up later:

```bash
microagent halt research
microagent start research
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

`halt` exits `0` on success; nonzero when the workspace cannot be found or the
clean shutdown fails. In AX mode a failure is written as a structured error
envelope.

## Related

- [`start`](/cli/start/) - boot the halted workspace again
- [`stop`](/cli/stop/) - signal a misbehaving VM instead
- [`kill`](/cli/kill/) - force-terminate when nothing returns
- [`status`](/cli/status/) - confirm the `halted` state
