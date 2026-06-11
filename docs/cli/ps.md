---
title: microagent ps
description: List every workspace and its current state.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

```text
microagent ps [--state-dir <dir>]
```

`ps` walks the state directory and prints one row per workspace, with name,
backend, and current state. It's the list view; for everything about one
workspace - readiness, verification, network detail - use
[`status`](/cli/status/).

## Examples

List workspaces:

```bash
microagent ps
microagent --json ps
```

Text output is one row per workspace:

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
| `--json` | Global flag before `ps`; print structured JSON output |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Exit status

`ps` exits `0` on success, including when no workspaces exist - a missing or
empty state directory lists zero rows rather than failing.

## Related

- [`status`](/cli/status/) for a single workspace
