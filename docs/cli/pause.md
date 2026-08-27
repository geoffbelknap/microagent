---
title: microagent pause
description: Freeze a running workspace in place, memory and all.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-27_

```text
microagent pause <name> [--reason <text>] [--state-dir <dir>]
```

`pause` freezes a running workspace and records its state as `paused`. The VM's
vCPUs stop executing, but guest memory, the workspace rootfs, attached disks,
identity, and `events.json` are all preserved. The runtime process keeps
running and the host-side network, port forwarding, and vsock paths stay in
place, so the workspace can be resumed in place with [`resume`](/cli/resume/).

The command uses the delayed terminal progress contract documented for
[`halt`](/cli/halt/). Short freezes finish without an indicator; JSON and MCP
responses remain structured.

This is memory pause, not a disk-preserving shutdown. Unlike [`halt`](/cli/halt/),
a paused workspace keeps its live memory state; `resume` continues exactly where
it left off rather than booting again.

While a workspace is paused, [`exec`](/cli/exec/), [`connect`](/cli/connect/),
and [`stats`](/cli/stats/) are rejected with a message directing you to resume
it first.

On Linux with the currently pinned Firecracker (v1.16.x), `exec` and the
model bridge stop answering after a bare pause/resume cycle — see
[Troubleshooting: `exec` times out after `pause` then `resume` on Linux](/troubleshooting/#exec-times-out-after-pause-then-resume-on-linux).

`pause` requires the workspace to be running.

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

`pause` exits `0` when the VM is frozen; nonzero when the workspace cannot be
found, is not running, or when the backend cannot freeze the VM.

## Related

- [`resume`](/cli/resume/) - thaw the paused workspace
- [`status`](/cli/status/) - confirm the `paused` state
- [`halt`](/cli/halt/) - disk-preserving shutdown instead (`stop` is an alias)
