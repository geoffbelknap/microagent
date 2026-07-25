---
title: microagent quarantine
description: Sever host-side workspace effects while preserving forensic state.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-25_

```text
microagent quarantine <name> [--no-capture] [--state-dir <dir>]
```

`quarantine` contains a workspace: it stops the runtime and severs every
host-side path, while preserving disk state, identity, runtime state files,
serial logs, and `events.json`, recording the state as `quarantined`.

It is the containment verb, not an operational shutdown. [`halt`](/cli/halt/)
parks a healthy workspace; `quarantine` records that the workspace was
*contained*, which is a governance action rather than routine lifecycle. Both
stop the VM and preserve the disk.

Quarantine removes host-side network paths, mediation listeners, published TCP
listeners, and console input where they exist, and deletes the guest-facing
socket endpoints so nothing stale survives to reconnect to. New connections
fail closed.

## Evidence is captured first

Stopping the runtime destroys the volatile state — running processes, open
connections, injected code, and any credential the workload obtained at
runtime. So `quarantine` takes a [forensic
snapshot](/cli/snapshot/#forensic-captures) **before** it severs, by default.
There is no plausible reason to want the other order, which is why it is the
default rather than a flag: capture is quiet, while severing a network is loud
enough for a hostile workload to notice and destroy evidence.

The capture retains guest secrets and is **not restorable**. It is evidence, so
keep it somewhere the workloads it came from cannot read. It appears in
[`snapshot list`](/cli/snapshot/) marked `retained` under `SECRETS`, and is
tagged `forensic-<timestamp>`.

The capture is **best-effort**. If it fails, the workspace is contained anyway
and the failure is reported — containment is never blocked by evidence
collection, or making capture fail would become a way to avoid being contained.

Pass `--no-capture` to contain without capturing and accept losing the volatile
state.

## Examples

Quarantine a workspace, inspect it, then take it down:

```bash
microagent quarantine research
microagent status research
microagent kill research
```

## Flags

`--no-capture` is the one worth knowing; `--state-dir` matters only when the
workspace lives outside the default `~/.microagent/`.

| Flag | Description |
|---|---|
| `--no-capture` | Contain without first capturing evidence (volatile state is lost) |
| `--name <name>` | Workspace name; positional name is also accepted |
| `--id <id>` | Workspace ID alias for `--name` |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--mode`/`--supervisor`.

## Exit status

`quarantine` exits `0` when the host-side effects are severed and the state is
recorded; nonzero when the workspace cannot be found or the backend cannot
sever its host-side effects. In AX mode a failure is written as a structured
error envelope.

## Related

- [`halt`](/cli/halt/) - park a healthy workspace instead (`stop` is an alias)
- [`kill`](/cli/kill/) - force-terminate a quarantined VM
- [`status`](/cli/status/) - confirm the `quarantined` state
- [State and identity](/concepts/state-and-identity/) - where `quarantined` sits in the lifecycle
