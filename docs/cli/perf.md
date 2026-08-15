---
title: microagent perf
description: Measure workspace performance.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-15_

```text
microagent perf boot [flags]               Measure boot time over iterations
microagent perf ready [flags]              Measure full readiness across lifecycle paths
microagent perf footprint <name> [flags]   Report backend process memory
microagent perf steady <name> [flags]      Sample steady-state memory over time
```

`perf` runs repeatable local measurements and reports structured results.
`ready` performs an excluded warm-up before its measured runs, shows phase
progress in text mode, and leads with the measured median and range. `boot`
creates disposable workspaces, waits for a guest command result, stops the
timer, and then removes the measured workspace. `footprint` reports the host
resident set size for the recorded backend process of a running workspace.
`steady` samples that RSS over time for steady-state overhead reporting.

`ready` measures the path to a securely usable workspace. Choose the lifecycle
transition with `--start`: a full cold boot, a fork from a reusable snapshot,
an in-place snapshot restore, or a paused-workspace resume. Choose the guest
interface with `--probe`: structured exec or the interactive shell. The timer
stops only after that interface completes the `--exec` command successfully.

By default, `ready` completes one full-path warm-up and then records five
measurements. The warm-up is excluded from every distribution. On a new host it
can seed the reusable rootfs baseline; on an already-used host it still keeps
one-time process and cache effects out of the reported samples. Use
`--warmups 0` only when those effects are part of the experiment.

Text output identifies the benchmark before it starts. It then shows the
active warm-up or measurement and its current phase on stderr. A terminal
updates one aligned duration in place and replaces the spinner with a check
when the run finishes. Redirected text emits stable lines without terminal
control sequences. JSON output stays quiet, so stdout contains only the final
structured report.

Boot and readiness reports carry their timer boundaries and excluded work as
structured fields. Snapshot capture, source-workspace boot, initial pause, and
iteration teardown never hide inside those numbers. Host page-cache state is
reported as `host_page_cache_uncontrolled`; do not describe these as cold-cache
measurements.

`boot` measures the pipeline `run` takes, cached rootfs baselines included. An
iteration clones a recorded baseline for the image when one matches, and the
first full build seeds one for the iterations and runs that follow. Those two
paths are different numbers: a full build pulls the image and makes a
filesystem, where a clone copies one. So each iteration reports which it took
in `rootfs` (`baseline` or `build`), counted in `summary.baselines` and
`summary.builds`. Read them before comparing runs: a report mixing both blends
a first-boot time into the average.

Its timer starts immediately before the one-shot workspace run and stops after
the guest command result is available. Workspace teardown happens afterward
and is listed in `boundary.excluded`; it is not boot or readiness work.

## Examples

Run the default human benchmark: one excluded warm-up followed by five
measurements.

```bash
microagent perf ready
```

Return a compact report for a script or agent:

```bash
microagent --json perf ready --summary
```

Measure three default boots:

```bash
microagent --json perf boot --iterations 3
```

Measure a prepared image from cold create through structured exec:

```bash
microagent --json perf ready \
  --image local/development:prepared \
  --start cold \
  --probe exec \
  --exec "test -w /workspace" \
  --network user \
  --iterations 20
```

Measure the same full-ready boundary when forking a pre-booted snapshot:

```bash
microagent --json perf ready \
  --image local/development:prepared \
  --start snapshot-fork \
  --probe exec \
  --exec "test -w /workspace" \
  --network user \
  --iterations 20
```

Measure in-place restore through a usable interactive shell:

```bash
microagent --json perf ready \
  --image local/development:prepared \
  --start snapshot-restore \
  --probe interactive \
  --exec "printf ready" \
  --iterations 20
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

## Commands

| Command | Purpose |
|---|---|
| `boot` | Measure disposable workspace boot time |
| `ready` | Measure a selected lifecycle transition through a successful guest command |
| `footprint` | Report host process RSS for a running workspace |
| `steady` | Sample host process RSS over time |

## Flags

Use the global `--json` flag (before `perf`) for the structured measurement
records shown in the examples above.

### `boot` flags

Common flags:

- `--iterations <n>` - one boot is noise; run several to get a usable min/avg/max
- `--image <ref>` - pin the image (by digest) so runs are comparable over time
- `--profile <name>` - measure the VM size you actually run, not the default
- `--exec <command>` - move the finish line from "guest up" to "workload ready"
- `--timeout <seconds>` - fail a hung iteration instead of stalling the whole run

The complete set:

| Flag | Description |
|---|---|
| `--image <ref>` | OCI image reference. Defaults to Python 3.13 slim |
| `--exec <command>` | Guest command used to mark boot completion. Defaults to `true` |
| `--iterations <n>` | Number of boot measurements. Defaults to 1 |
| `--profile <name>` | Resource profile: `tiny`, `small`, `medium`, or `large` |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |
| `--timeout <seconds>` | Per-iteration timeout |
| `--mke2fs <path>` | mke2fs binary path |
| `--debugfs <path>` | debugfs binary path, used after `mke2fs` to preserve the image's declared uid/gid and mode bits |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--network <mode>` | Network mode for measured boots (`user`, `isolated`); empty uses the backend default. Isolated boots need no host network privileges |

### `ready` flags

`ready` accepts the same flags as `boot`, plus `--start` and `--probe`. Its
`--exec` command runs only after the measured lifecycle transition; it is never
baked into the rootfs or the reusable snapshot.

| Flag | Description |
|---|---|
| `--start <mode>` | Lifecycle transition to measure: `cold`, `snapshot-fork`, `snapshot-restore`, or `paused-resume`. Defaults to `cold` |
| `--probe <interface>` | Guest interface that must complete the command: `exec` or `interactive`. Defaults to `interactive` |
| `--warmups <n>` | Excluded full-path warm-up runs. Defaults to 1; `0` disables warm-up |
| `--iterations <n>` | Recorded measurements after warm-up. Defaults to 5 |
| `--summary` | With JSON output, omit per-iteration and host details |

Canonical JSON values are `cold_boot`, `snapshot_fork`, `snapshot_restore`, and
`paused_resume` in `start_mode`; `structured_exec` and `interactive_shell` in
`readiness_probe`.

Every iteration reports these phase fields:

| Field | Meaning |
|---|---|
| `rootfs_prepare_ms` | Cold-boot private reflink/copy time; a subset of workspace preparation |
| `workspace_prepare_ms` | Cold-boot create, rootfs derivation, config disk, verification, manifest, and supervisor preparation |
| `lifecycle_ms` | Selected lifecycle request: cold create+start, snapshot fork, snapshot restore, or paused resume |
| `interface_ready_ms` | Successful no-op through structured exec or the interactive shell after the lifecycle request returns |
| `runtime_ready_ms` | Timer start through successful interface readiness |
| `probe_ms` | Successful `--exec` command through the selected guest interface |
| `duration_ms` | End-to-end time through the probe; teardown excluded |

`ok` and `error` describe the measured lifecycle and probe. Cleanup happens
outside that boundary: `teardown_error` records an iteration cleanup failure
without discarding a successful measurement, and the summary reports the count
as `teardown_failures`. The command still exits nonzero when teardown fails so
a leaked benchmark workspace cannot pass silently.

The summary reports `min_ms`, `avg_ms`, `p50_ms`, `p95_ms`, and `max_ms` distributions
for every phase above. `full_ready_ms` is the distribution of iteration
`duration_ms` values. A prepared cold-boot run should contain only
`rootfs: "baseline"` iterations. A `build` iteration includes image realization
and is not a prepared-image readiness sample. Restore, fork, and resume reports
record their one-time source preparation under `setup` with `excluded: true`.
Reusable setup uses a structured-exec no-op, recorded in
`setup.readiness_probe`; it does not pre-open the interface selected for the
measured iteration.

Full JSON records excluded runs under `warmup.iterations` and their aggregate
under `warmup.summary`. The measured `iterations` array and top-level `summary`
never include warm-ups. `--summary` keeps the benchmark configuration, timer
boundary, warm-up counts, and measured phase distributions while omitting the
iteration arrays and host capability record.

Human output leads with the measured median and range. Its breakdown uses the
actual median run and reports exclusive lifecycle phases, so the rows describe
one coherent path rather than independently aggregated phase statistics. The
configuration, success counts, rootfs provenance, cache condition, timer, and
exclusions follow in one details block. Human output shows p95 only when at
least 20 measurements succeeded; with fewer samples, p95 is effectively the
maximum and is not a useful headline.

The `boundary` object states the exact timer edges and exclusions. This keeps
the labels honest:

- `cold_boot` starts before workspace creation. It includes private rootfs
  derivation, VM launch, guest boot, interface readiness, and the command.
- `snapshot_fork` starts before creating a new workspace from a pre-booted
  snapshot. Source boot and snapshot capture are excluded.
- `snapshot_restore` starts before restoring the same stopped workspace from
  its pre-booted snapshot. Source boot and snapshot capture are excluded.
- `paused_resume` starts before thawing the existing VM. Initial boot and pause
  are excluded.

An interactive result is not merely a connected socket: the interactive shell
must complete a command. An exec result likewise requires a decoded structured
result with status `exited` and exit code `0`.

### `footprint` flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |

### `steady` flags

| Flag | Description |
|---|---|
| `--duration <seconds>` | Sampling duration. Defaults to 10 |
| `--interval <seconds>` | Sampling interval. Defaults to 1 |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--supervisor`.

## Exit status

`perf` exits `0` when every measurement completes; nonzero when a boot or ready
iteration fails or times out, or when `footprint`/`steady` cannot find a
running workspace process to sample. `boot` still prints the full report
before exiting nonzero - failed iterations are recorded per-iteration (`ok`,
`error`) and counted in `summary.failures`, so CI can gate on the exit code
without losing the measurements. An iteration that failed before it built or
cloned a rootfs reports an empty `rootfs`.

## Reference measurements

Boot time and footprint are host- and image-dependent: CPU generation,
whether you're on bare metal or a nested hypervisor (WSL2 included), the
installed kernel, and which OCI image you measure against all move the
numbers. There is no single "microagent boot time" - there's the number on
your host, with your image, today. Measure your own rather than trusting a
number that traveled from someone else's machine.

Produce it with the same commands the examples above use. Run `boot` with
enough iterations to smooth out first-boot cache-fill noise — 10 is a
reasonable default; raise it if you're chasing a tail latency and want a
steadier `max_ms`. When a baseline for the image is already recorded (by any
earlier `run`, `create`, or `image pull` of it) every iteration clones and
`summary.builds` is `0`, which is the steadiest report to compare over time.
Measure `footprint` against a workspace you've started, not just created:

```bash
# boot: min/avg/max over repeated disposable boots. Isolated network needs
# no host network privileges.
microagent --json perf boot \
  --image docker.io/library/nats@sha256:<digest> \
  --profile tiny \
  --network isolated \
  --iterations 10

# footprint: host RSS for a workspace you've started
microagent create bench --image docker.io/library/nats@sha256:<digest> --profile tiny --network isolated
microagent start bench
microagent --json perf footprint bench
microagent halt bench && microagent delete bench --yes
```

For a one-command measurement that also prints host context (CPU model, RAM,
kernel, architecture), source commit and dirty-tree state, run
[`scripts/dev/perf-snapshot.sh`](https://github.com/geoffbelknap/microagent/blob/main/scripts/dev/perf-snapshot.sh)
from a checkout. It builds the CLI (or reuses one you point it at via
`MICROAGENT_CLI`). It runs the full eight-lane lifecycle/interface readiness
matrix plus boot and footprint measurements against a pinned image. It cleans
up its disk-backed scratch state before it exits. The summary names and hashes
the CLI, guest init, supervisor, and VMM it actually measured; checkout-built
runs also reject a mismatched guest init or supervisor. That output block is the
right shape to paste into an issue, a PR description, or a report of your own
measured numbers - it carries the timing boundary and context a bare number
doesn't.

This page deliberately stops at "how to measure." Overhead relative to
other tools is a different question with a different answer on every host
and image; it isn't answered here.

## Related

- [`run`](/cli/run/) - the one-shot path `perf boot` measures
- [`connect`](/cli/connect/) - the interactive path `perf ready` probes
- [`stats`](/cli/stats/) - live resource usage for one workspace
- [`host`](/cli/host/) - host backend capabilities that affect boot time
- [`status`](/cli/status/) - inspect a running workspace before `footprint`/`steady`
