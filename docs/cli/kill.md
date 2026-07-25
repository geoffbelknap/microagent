---
title: microagent kill
description: Force-terminate a workspace that won't stop.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-25_

```text
microagent kill <name> [--state-dir <dir>]
```

`kill` is the hard variant of [`halt`](/cli/halt/). Use it when a graceful
`halt` doesn't return within its graceful window; `halt` never escalates on its
own. For a clean shutdown of a healthy workspace you intend to start again, use
[`halt`](/cli/halt/) (or its `stop` alias) instead. The disk state survives
`kill`, but nothing inside the guest gets a chance to flush or exit cleanly.

## Examples

Force-terminate a workspace:

```bash
microagent kill research
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

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--supervisor`.

## Exit status

`kill` exits `0` on success; nonzero when the workspace cannot be found or the
VM process cannot be terminated.

## Related

- [`halt`](/cli/halt/) - the graceful variant (park a healthy workspace cleanly; `stop` is an alias)
- [`delete`](/cli/delete/) - remove the workspace afterwards
