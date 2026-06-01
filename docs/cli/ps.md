---
title: microagent ps
description: List all workspaces in the state directory.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

```text
microagent ps [--state-dir <dir>]
```

`ps` walks the state directory and prints one row per workspace, with name,
backend, and current state.

## Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory to scan (default `~/.microagent/`) |
| `--json` | Global flag before `ps`; print structured JSON output |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Example

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

## Related

- [`status`](/cli/status/) for a single workspace
