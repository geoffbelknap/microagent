---
title: microagent kill
description: Force-terminate a workspace that won't stop.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-15_

```text
microagent kill <name> --reason <text> [--yes] [--state-dir <dir>]
```

`kill` is the hard variant of [`halt`](halt.md). Use it when a graceful
`halt` doesn't return within its graceful window; `halt` never escalates on its
own. For a clean shutdown of a healthy workspace you intend to start again, use
[`halt`](halt.md) (or its `stop` alias) instead. The disk state survives
`kill`, but nothing inside the guest gets a chance to flush or exit cleanly.

Terminal presentation follows the delayed lifecycle progress behavior described
for [`halt`](halt.md). A quick force-termination remains quiet, and structured
output contains no presentation text.

Because it discards volatile runtime state, `kill` requires an audit reason and
asks for confirmation when the workspace is live. Use `--yes` only after the
caller has made that decision through another interaction or authorization
step. [`halt`](halt.md) remains immediate and does not require confirmation.

## Examples

Force-terminate a workspace after confirming the prompt:

```bash
microagent kill research --reason "guest did not halt"
```

## Flags

`--state-dir` matters only when the workspace lives outside the default
`~/.microagent/`.

| Flag | Description |
|---|---|
| `--name <name>` | Workspace name; positional name is also accepted |
| `--id <id>` | Workspace ID alias for `--name` |
| `--reason <text>` | Opaque reason recorded as the lifecycle event's `purpose` |
| `--yes`, `-y` | Confirm without prompting; intended for deliberate automation |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](index.md#global-flags) for `--output`/`--json`/`--supervisor`.

## Exit status

`kill` exits `0` on success; nonzero when the workspace cannot be found or the
VM process cannot be terminated.

## Related

- [`halt`](halt.md) - the graceful variant (park a healthy workspace cleanly; `stop` is an alias)
- [`delete`](delete.md) - remove the workspace afterwards
