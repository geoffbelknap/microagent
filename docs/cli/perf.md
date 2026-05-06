---
title: microagent perf
description: Measure workspace performance.
---

```text
microagent perf boot [flags]
```

`perf` runs repeatable local measurements and reports structured results. The
first benchmark is `boot`, which creates disposable workspaces, waits for a
guest command to complete, and reports per-iteration duration plus min/avg/max.

## Commands

| Command | Purpose |
|---|---|
| `boot` | Measure disposable workspace boot time |

## `boot` Flags

| Flag | Description |
|---|---|
| `--image <ref>` | OCI image reference. Defaults to the small BusyBox baseline |
| `--exec <command>` | Guest command used to mark boot completion. Defaults to `true` |
| `--iterations <n>` | Number of boot measurements. Defaults to 1 |
| `--profile <name>` | Resource profile: `tiny`, `small`, `medium`, or `large` |
| `--state-dir <dir>` | State directory |
| `--timeout <seconds>` | Per-iteration timeout |
| `--mke2fs <path>` | mke2fs binary path |
| `--supervisor <path>` | Override the active backend supervisor path |

## Examples

Measure three default boots:

```bash
microagent --json perf boot --iterations 3
```

Measure a pinned Ubuntu image with the tiny profile:

```bash
microagent --json perf boot \
  --image docker.io/library/ubuntu@sha256:<digest> \
  --profile tiny \
  --iterations 5
```
