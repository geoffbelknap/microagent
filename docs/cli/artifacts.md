---
title: microagent artifacts
description: List and retrieve declared workspace artifacts.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

```text
microagent artifacts <name> [--state-dir <dir>]
microagent artifacts get <name> <artifact> <target> [--state-dir <dir>] [--debugfs <path>]
```

`artifacts` reports the input bundles and output paths declared in the
workspace manifest. `artifacts get` retrieves a declared output artifact by
name without entering the workspace - the host reads it straight off the
workspace disk. Only declared `outputs` are retrievable by artifact name; for
arbitrary file copying, use [`cp`](/cli/cp/).

## Examples

List declared artifacts, then fetch one by name:

```bash
microagent --json artifacts research
microagent artifacts get research report ./out/
```

If an output path sits under an attached disk mountpoint, `artifacts get`
reads from that disk. Otherwise it reads from the rootfs image.

## Flags

You'll rarely need flags here - `--debugfs` only when the `debugfs` binary
lives somewhere unusual.

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |
| `--debugfs <path>` | debugfs binary path for `artifacts get` |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Exit status

`artifacts` exits `0` when the workspace manifest is found and read; nonzero
when the workspace cannot be found, the named artifact is not declared, or the
read from the workspace disk fails. In AX mode a failure is written as a
structured error envelope.

## Related

- [`run`](/cli/run/) - declare outputs with `--output`
- [`create`](/cli/create/) - declare outputs in the workspace spec
- [`cp`](/cli/cp/) - copy arbitrary files instead
- [`status`](/cli/status/) - declared artifacts appear under `artifacts`
