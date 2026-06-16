---
title: microagent list
description: List saved workspaces and their current state.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-16_

```text
microagent list [--state-dir <dir>]
microagent ls [--state-dir <dir>]
```

`list` walks the state directory and prints one row per saved workspace, with
name, backend, and current state. It is an inventory view, so stopped
workspaces appear because their disks and state still exist. To show only live
VMs, use [`ps`](/cli/ps/). For everything about one workspace - readiness,
verification, network detail - use
[`status`](/cli/status/).

`ls` is an alias for `list`.

## Examples

List saved workspaces:

```bash
microagent list
microagent ls
microagent --json list
```

Text output is one row per saved workspace:

```text
NAME                     STATE        BACKEND      PROFILE      NETWORK    RESTART
research                 running      firecracker  medium       nat        on-failure
template                 stopped      firecracker  small        user       never
```

With `--json`, the rows are returned under `workspaces`:

```json
{
  "workspaces": [
    {
      "name": "research",
      "state": "running",
      "backend": "firecracker",
      "profile": "medium",
      "restart": "on-failure",
      "network": "nat",
      "observed_at": "2026-06-01T12:00:00Z"
    }
  ]
}
```

## Flags

You'll rarely need flags here - `--state-dir` only when your workspaces live
outside the default `~/.microagent/`.

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory to scan (default `~/.microagent/`) |
| `--json` | Global flag before `list`; print structured JSON output |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Exit status

`list` exits `0` on success, including when no workspaces exist - a missing or
empty state directory lists zero rows rather than failing. In AX mode a
failure is written as a structured error envelope.

## Related

- [`status`](/cli/status/) - the deep view of a single workspace
- [`ps`](/cli/ps/) - show only running workspaces
