---
title: microagent cp
description: Copy regular files into or out of stopped workspace disks.
---

```text
microagent cp <source> <target> [--state-dir <dir>] [--debugfs <path>]
```

`cp` copies one regular file between the host and an offline workspace disk.
It is not a sync daemon and it does not attach to a running VM.

## Endpoints

Exactly one endpoint must be a workspace endpoint:

| Form | Meaning |
|---|---|
| `<workspace>:/absolute/path` | Rootfs path |
| `<workspace>:<disk>:/absolute/path` | Attached disk path |
| `/host/path` | Host path |

## Semantics

- The workspace must be prepared or stopped.
- Only regular files are supported.
- Workspace paths must be absolute file paths.
- Copying from a workspace to a host directory writes a file with the same
  basename as the workspace path.
- Attached disk names refer to the `--disk` or `--bundle` names recorded in
  the workspace manifest.
- The implementation uses `debugfs`; pass `--debugfs` when it is not on `PATH`.

## Examples

Copy into the rootfs:

```bash
microagent cp ./config.json research:/etc/microagent/config.json
```

Copy from the rootfs:

```bash
microagent cp research:/var/log/boot.log ./boot.log
```

Copy into an attached disk named `workspace`:

```bash
microagent cp ./notes.txt research:workspace:/notes.txt
```

## Related

- [`create`](create.md)
- [`clone`](clone.md)
- [`logs`](logs.md)
