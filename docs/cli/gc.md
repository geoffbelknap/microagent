---
title: microagent gc
description: Reap dead VM processes and stale workspace state.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-23_

```text
microagent gc [--state-dir <dir>]
```

`gc` scans the state directory for workspaces recorded as `running` and
reconciles each one against reality: if the recorded VM process is gone, or
its `--ttl` idle lease has expired, `gc` tears down the leftover process
group and releases stale runtime artifacts - port-forward and vsock listener
processes, the egress mediator, and transient firewall rules and network
devices - then records the workspace's terminal state. A workspace whose
process is still alive and in-lease is left alone. Run `gc` when
[`ps`](/cli/ps/) and reality disagree - after a host crash, or when a
supervisor exited without cleanup. It does not delete workspace disks or
identity: that is [`delete`](/cli/delete/).

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

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--mode`/`--supervisor`.

## Exit status

`gc` exits `0` on success, including when nothing needed reaping. Reap
failures on individual workspaces are logged to stderr without failing the
overall run; `gc` exits nonzero only when the state directory itself cannot
be read. In AX mode a failure is written as a structured error envelope.

## Related

- [`ps`](/cli/ps/) - see what is actually running
- [`delete`](/cli/delete/) - remove a workspace and its disk state
