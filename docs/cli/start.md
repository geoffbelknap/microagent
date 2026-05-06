---
title: microagent start
description: Boot a previously created workspace.
---

```text
microagent start <name> [--state-dir <dir>]
```

`start` boots a workspace that was previously created. The workspace must
exist in the state directory (default `~/.microagent/`).

## Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory holding the workspace record |
| `--profile <name>` | Resource profile override: `tiny`, `small`, `medium`, or `large` |
| `--memory <MiB>` | Memory override for this start |
| `--cpus <n>` | CPU count override for this start |
| `--supervisor <path>` | Override the active backend supervisor path |

## Example

```bash
microagent start research
```

`start` reuses the resource config stored by `create`. Pass `--profile`,
`--memory`, or `--cpus` only when you want a one-start override.

After it's running, open a console with [`connect`](/cli/connect/) on Apple
VF, or read serial output with [`logs`](/cli/logs/).

## Related

- [`create`](/cli/create/), [`stop`](/cli/stop/), [`status`](/cli/status/)
