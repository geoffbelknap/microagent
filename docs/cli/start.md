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
| `--supervisor <path>` | Override the active backend supervisor path |

## Example

```bash
microagent start research
```

After it's running, open a console with [`connect`](/cli/connect/), or read
serial output with [`logs`](/cli/logs/).

## Related

- [`create`](/cli/create/), [`stop`](/cli/stop/), [`status`](/cli/status/)
