---
title: microagent perf
description: Measure workspace performance.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-03_

```text
microagent perf boot [flags]               Measure boot time over iterations
microagent perf footprint <name> [flags]   Report backend process memory
microagent perf steady <name> [flags]      Sample steady-state memory over time
```

`perf` runs repeatable local measurements and reports structured results.
`boot` creates disposable workspaces, waits for a guest command to complete,
and reports per-iteration duration plus min/avg/max. `footprint` reports the
host resident set size for the recorded backend process of a running workspace.
`steady` samples that RSS over time for steady-state overhead reporting.

`boot` measures the pipeline `run` takes, cached rootfs baselines included. An
iteration clones a recorded baseline for the image when one matches, and the
first full build seeds one for the iterations and runs that follow. Those two
paths are different numbers: a full build pulls the image and makes a
filesystem, where a clone copies one. So each iteration reports which it took
in `rootfs` (`baseline` or `build`), counted in `summary.baselines` and
`summary.builds`. Read them before comparing runs: a report mixing both blends
a first-boot time into the average.

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

## Commands

| Command | Purpose |
|---|---|
| `boot` | Measure disposable workspace boot time |
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

`perf` exits `0` when every measurement completes; nonzero when a boot
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

For a one-command version of the same measurement that also prints host
context (CPU model, RAM, kernel, architecture) and the microagent commit
under test, run
[`scripts/dev/perf-snapshot.sh`](https://github.com/geoffbelknap/microagent/blob/main/scripts/dev/perf-snapshot.sh)
from a checkout. It builds the CLI (or reuses one you point it at via
`MICROAGENT_CLI`), runs both measurements against a pinned image, and cleans
up the workspace and scratch state it created before it exits. That output
block is the right shape to paste into an issue, a PR description, or a
report of your own measured numbers - it carries the context a bare number
doesn't.

This page deliberately stops at "how to measure." Overhead relative to
other tools is a different question with a different answer on every host
and image; it isn't answered here.

## Related

- [`run`](/cli/run/) - the one-shot path `perf boot` measures
- [`stats`](/cli/stats/) - live resource usage for one workspace
- [`host`](/cli/host/) - host backend capabilities that affect boot time
- [`status`](/cli/status/) - inspect a running workspace before `footprint`/`steady`
