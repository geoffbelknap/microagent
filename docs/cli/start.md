---
title: microagent start
description: Boot a previously created workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-17_

```text
microagent start <name> [--state-dir <dir>]
```

`start` boots a workspace that was previously created. The workspace must
exist in the state directory (default `~/.microagent/`).

Start is disk-state resume, not memory resume. It boots from the persisted
workspace disk after `prepared`, `halted`, `stopped`, or `failed`. It rejects
workspaces that are already `starting` or `running`.

`quarantined` is intentionally distinct: host-side network, mediation, and
side-effect paths were severed while the runtime may still exist. Run `halt`,
`stop`, or `kill` first, then `start` from the preserved disk state.

## Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory holding the workspace record |
| `--profile <name>` | Resource profile override: `tiny`, `small`, `medium`, or `large` |
| `--memory <MiB>` | Memory override for this start |
| `--cpus <n>` | CPU count override for this start |
| `--kernel <path>` | Linux kernel path override |
| `--arch <arch>` | Guest architecture |
| `--backend <name>` | Backend identity override |
| `--vsock p=host:port` | Add a vsock mapping for this start. Repeatable |
| `--supervisor <path>` | Override the installed host backend supervisor path |

## Example

```bash
microagent start research
```

`start` reuses the resource config stored by `create`. Pass `--profile`,
`--memory`, or `--cpus` only when you want a one-start override.

After it's running, open a console with [`connect`](/cli/connect/) on Apple
VF, Firecracker, or Windows Hyper-V, or read serial output with
[`logs`](/cli/logs/).

## Related

- [`create`](/cli/create/), [`stop`](/cli/stop/), [`status`](/cli/status/)
