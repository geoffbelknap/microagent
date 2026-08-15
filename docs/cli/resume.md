---
title: microagent resume
description: Thaw a paused workspace back to running, exactly where it was.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-15_

```text
microagent resume <name> [--reason <text>] [--state-dir <dir>]
```

`resume` thaws a [`paused`](/cli/pause/) workspace and records its state as
`running` again. The VM's vCPUs continue executing from exactly where they were
frozen, with guest memory, disk state, and the host-side network, port
forwarding, and vsock paths intact. After resume, [`exec`](/cli/exec/),
[`connect`](/cli/connect/), and [`stats`](/cli/stats/) work again.

If the backend call takes long enough to notice, human output shows delayed
progress on stderr. Fast resumes remain quiet; JSON and MCP output stays
structured.

`resume` requires the workspace to be paused. To boot a halted or stopped
workspace from disk, use [`start`](/cli/start/).

## Examples

Freeze a workspace, then thaw it:

```bash
microagent pause research
microagent resume research
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

`resume` exits `0` when the VM is running again; nonzero when the workspace
cannot be found, is not paused, or when the backend cannot thaw the VM.

## Related

- [`pause`](/cli/pause/) - freeze the workspace first
- [`status`](/cli/status/) - confirm it is `running` again
- [`start`](/cli/start/) - boot halted or stopped workspaces from disk
