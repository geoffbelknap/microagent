---
title: microagent stop
description: Signal a workspace to shut down gracefully.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-27_

```text
microagent stop <name> [--state-dir <dir>]
```

`stop` asks a workspace to shut down gracefully. If the VM hasn't exited after
five seconds, `stop` marks the workspace `failed` and returns an error. It
never escalates on its own; follow up with [`kill`](/cli/kill/) when you need a
hard termination. When you're parking a healthy workspace to start again later,
prefer [`halt`](/cli/halt/), which records the clean `halted` state.

## Examples

Stop a workspace:

```bash
microagent stop research
```

If the VM doesn't shut down within the deadline, force it:

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

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`/`--supervisor`.

## Exit status

`stop` exits `0` on success; nonzero when the workspace cannot be found or when
the VM does not exit within the five-second deadline (the workspace is then
marked `failed`). In AX mode a failure is written as a structured error
envelope.

## Related

- [`halt`](/cli/halt/) - park a healthy workspace cleanly
- [`kill`](/cli/kill/) - force-terminate when `stop` can't
- [`delete`](/cli/delete/) - remove the workspace entirely
- [`status`](/cli/status/) - confirm the resulting state
