---
title: microagent perf
description: Measure workspace performance.
---

```text
microagent perf boot [flags]
microagent perf footprint <name> [flags]
microagent perf steady <name> [flags]
```

`perf` runs repeatable local measurements and reports structured results. The
`boot` creates disposable workspaces, waits for a guest command to complete,
and reports per-iteration duration plus min/avg/max. `footprint` reports the
host resident set size for the recorded backend process of a running workspace.
`steady` samples that RSS over time for steady-state overhead reporting.

## Commands

| Command | Purpose |
|---|---|
| `boot` | Measure disposable workspace boot time |
| `footprint` | Report host process RSS for a running workspace |
| `steady` | Sample host process RSS over time |

## `boot` Flags

| Flag | Description |
|---|---|
| `--image <ref>` | OCI image reference. Defaults to Python 3.13 slim |
| `--exec <command>` | Guest command used to mark boot completion. Defaults to `true` |
| `--iterations <n>` | Number of boot measurements. Defaults to 1 |
| `--profile <name>` | Resource profile: `tiny`, `small`, `medium`, or `large` |
| `--state-dir <dir>` | State directory |
| `--timeout <seconds>` | Per-iteration timeout |
| `--mke2fs <path>` | mke2fs binary path |
| `--supervisor <path>` | Override the installed host backend supervisor path |

## `footprint` Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory |

## `steady` Flags

| Flag | Description |
|---|---|
| `--duration <seconds>` | Sampling duration. Defaults to 10 |
| `--interval <seconds>` | Sampling interval. Defaults to 1 |
| `--state-dir <dir>` | State directory |

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

Report host RSS for a running workspace:

```bash
microagent --json perf footprint research
```

Sample steady-state RSS for one minute:

```bash
microagent --json perf steady research --duration 60 --interval 5
```
