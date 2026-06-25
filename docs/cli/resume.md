---
title: microagent resume
description: Thaw a paused workspace back to running, exactly where it was.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-25_

```text
microagent resume <name> [--state-dir <dir>]
```

`resume` thaws a [`paused`](/cli/pause/) workspace and records its state as
`running` again. The VM's vCPUs continue executing from exactly where they were
frozen, with guest memory, disk state, and the host-side network, port
forwarding, and vsock paths intact. After resume, [`exec`](/cli/exec/),
[`connect`](/cli/connect/), and [`stats`](/cli/stats/) work again.

`resume` requires the workspace to be paused - to boot a halted or stopped
workspace from disk, use [`start`](/cli/start/). Pause/resume is implemented on
Firecracker and Apple VF.

## Examples

Freeze a workspace, then thaw it:

```bash
microagent pause research
microagent resume research
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

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`/`--supervisor`.

## Exit status

`resume` exits `0` when the VM is running again; nonzero when the workspace
cannot be found, is not paused, or when the backend cannot thaw the VM. In AX
mode a failure is written as a structured error envelope.

## Related

- [`pause`](/cli/pause/) - freeze the workspace first
- [`status`](/cli/status/) - confirm it is `running` again
- [`start`](/cli/start/) - boot halted or stopped workspaces from disk
