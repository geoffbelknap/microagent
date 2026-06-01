---
title: microagent stats
description: Show or stream resource usage for a running workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

```text
microagent stats <name> [--follow] [--state-dir <dir>]
```

`stats` reports CPU, memory, and I/O for a running workspace, sampled from the
host view of the backing VM monitor process (similar to how `docker stats` reads
container resource accounting). CPU percent is measured across a short interval
and can exceed 100% for a multi-vCPU workspace.

By default `stats` prints one sample. With `--follow` (`-f`) it streams samples
about once a second until the workspace stops or you interrupt with Ctrl-C. With
the global `--json` flag a single sample is returned as a JSON object; `--follow`
is not supported with JSON/AX output.

The workspace must be running; `stats` on a stopped workspace is an error.

## Flags

| Flag | Description |
|---|---|
| `--follow`, `-f` | Stream samples until the workspace stops or you interrupt |
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Example

```bash
microagent stats research
```

```text
pid=48213  cpu=4.5%  mem=256.0 MiB  io_read=12.0 MiB  io_write=3.5 MiB
```

```bash
microagent --json stats research
```

```json
{
  "pid": 48213,
  "cpuPercent": 4.5,
  "memoryBytes": 268435456,
  "ioReadBytes": 12582912,
  "ioWriteBytes": 3670016,
  "sampledAt": "2026-06-01T20:30:00Z"
}
```

## Related

- [`perf`](/cli/perf/) for boot and footprint benchmarking
- [`status`](/cli/status/) for state and readiness
