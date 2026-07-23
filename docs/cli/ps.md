---
title: microagent ps
description: List running workspaces.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-23_

```text
microagent ps [--state-dir <dir>]
```

`ps` prints the live workspace view: workspaces whose VM state is starting,
running, paused, quarantined, or stopping. It does not show stopped persistent
workspaces. Use [`list`](/cli/list/) for the full saved-workspace inventory.

## Examples

Show running workspaces:

```bash
microagent ps
microagent --json ps
```

Text output uses the same columns as `list`:

```text
NAME                     STATE        BACKEND      PROFILE      NETWORK    RESTART
research                 running      linux-kvm    medium       user       on-failure
```

When no VMs are live, the text output is:

```text
No workspaces.
```

With `--json`, the rows are returned under `workspaces`.

## Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory to scan (default `~/.microagent/`) |
| `--json` | Global flag before `ps`; print structured JSON output |

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--mode`.

## Related

- [`list`](/cli/list/) - full saved-workspace inventory
- [`status`](/cli/status/) - detailed state for one workspace
