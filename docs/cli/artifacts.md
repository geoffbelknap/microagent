---
title: microagent artifacts
description: List and retrieve declared workspace artifacts.
---

```text
microagent artifacts <name> [--state-dir <dir>]
microagent artifacts get <name> <artifact> <target> [--state-dir <dir>] [--debugfs <path>]
```

`artifacts` reports declared input bundles and output paths from the workspace
manifest. `artifacts get` retrieves a declared output artifact by name without
entering the workspace.

Only declared `outputs` are retrievable by artifact name. For arbitrary file
copying, use [`cp`](/cli/cp/).

## Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory |
| `--debugfs <path>` | debugfs binary path for `artifacts get` |
| `--json` | Print structured JSON output |

## Examples

```bash
microagent artifacts research --json
microagent artifacts get research report ./out/
```

If an output path is under an attached disk mountpoint, `artifacts get` reads
from that disk. Otherwise it reads from the rootfs image.

## Related

- [`create`](/cli/create/)
- [`run`](/cli/run/)
- [`cp`](/cli/cp/)
- [`status`](/cli/status/)
