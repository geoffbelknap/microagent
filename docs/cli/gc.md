---
title: microagent gc
description: Reap dead VM processes and stale workspace state.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-15_

```text
microagent gc [--state-dir <dir>]
```

`gc` scans the state directory for workspaces recorded as `running` and
reconciles each one against reality. If the recorded VM process is gone, or
its start-time `--ttl` lifetime lease has expired, `gc` tears down the leftover process
group, releases stale runtime artifacts, and records the workspace's terminal
state. The stale artifacts are the port-forward and vsock listener processes,
the egress mediator, and transient firewall rules and network devices. A
workspace whose process is still alive and in-lease is left alone. Run `gc` when
[`ps`](ps.md) and reality disagree - after a host crash, or when a
supervisor exited without cleanup. It does not delete workspace disks or
identity: that is [`delete`](delete.md).

The sweep reports one bounded checked/total counter on stderr when it takes
long enough to notice. It continues after a per-workspace reconciliation
failure, includes those failures in the `failed` result array, and exits
nonzero after the complete batch. JSON output contains no terminal progress
text.

## Examples

Reap dead processes and stale state in the default state directory:

```bash
microagent gc
```

See what got reaped, structured:

```bash
microagent --json gc
```

## Flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory to scan (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](index.md#global-flags) for `--output`/`--json`/`--supervisor`.

## Exit status

`gc` exits `0` on success, including when nothing needed reaping. Reap
failures on individual workspaces are logged to stderr without failing the
overall run; `gc` exits nonzero only when the state directory itself cannot
be read.

## Related

- [`ps`](ps.md) - see what is actually running
- [`delete`](delete.md) - remove a workspace and its disk state
