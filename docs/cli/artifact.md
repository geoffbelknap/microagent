---
title: microagent artifact
description: List and retrieve declared workspace artifacts.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-15_

```text
microagent artifact <name> [--state-dir <dir>]                                              List declared artifacts
microagent artifact get <name> <artifact> <target> [--state-dir <dir>] [--debugfs <path>]   Retrieve one output artifact
```

`artifact` reports the input bundles and output paths declared in the
workspace manifest. `artifact get` retrieves a declared output artifact by
name without entering the workspace. The host reads it straight off the
workspace disk, so the workspace must be prepared, halted, or stopped -
`artifact get` fails on a running workspace. Only declared `outputs` are
retrievable by artifact name; for arbitrary file copying, use
[`cp`](/cli/cp/).

In a terminal, `artifact get` reports its current phase and transferred bytes
on stderr when retrieval takes long enough to notice. The completed byte count
uses the retrieved file size as its total. JSON and MCP responses remain
structured and contain no terminal progress text.

## Examples

List declared artifacts, then fetch one by name:

```bash
microagent --json artifact research
microagent artifact get research report ./out/
```

If an output path sits under an attached disk mountpoint, `artifact get`
reads from that disk. Otherwise it reads from the rootfs image.

## Flags

`--debugfs` matters only when the `debugfs` binary lives somewhere unusual.

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |
| `--debugfs <path>` | debugfs binary path for `artifact get` |

See [global flags](/cli/#global-flags) for `--output`/`--json`.

## Exit status

`artifact` exits `0` when the workspace manifest is found and read. It exits
nonzero when the workspace cannot be found, the named artifact is not
declared, the workspace is running (for `artifact get`), or the read from the
workspace disk fails.

## Related

- [`run`](/cli/run/) - declare outputs with `--output`
- [`create`](/cli/create/) - declare outputs in the workspace spec
- [`cp`](/cli/cp/) - copy arbitrary files instead
- [`status`](/cli/status/) - declared artifacts appear under `artifacts`
