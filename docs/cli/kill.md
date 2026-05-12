---
title: microagent kill
description: Force-stop a workspace.
---

```text
microagent kill <name> [--state-dir <dir>]
```

`kill` is the hard variant of [`stop`](/cli/stop/). On Firecracker it sends
SIGKILL to the recorded VM process; on Apple VF it asks the supervisor to
terminate the VM immediately. Use it when `stop` doesn't return.

## Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory holding the workspace record |
| `--supervisor <path>` | Override the installed host backend supervisor path |

## Example

```bash
microagent kill research
```

## Related

- [`stop`](/cli/stop/), [`delete`](/cli/delete/)
