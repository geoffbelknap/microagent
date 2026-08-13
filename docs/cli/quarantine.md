---
title: microagent quarantine
description: Sever host-side workspace effects while preserving forensic state.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-13_

```text
microagent quarantine <name> --reason <text> [--yes] [--no-capture] [--state-dir <dir>]
```

`quarantine` contains a workspace in one ordered operation: it creates a
durable deny marker, freezes guest execution, severs every host-side authority
path, captures evidence while the guest remains frozen, and then stops the
runtime into custody. Disk state, identity, runtime state files, serial logs,
events, the phase result, and the forensic capture remain available.

It is the containment verb, not an operational shutdown. [`halt`](/cli/halt/)
parks a healthy workspace; `quarantine` records that the workspace was
*contained*, which is a governance action rather than routine lifecycle. A
contained workspace cannot be started, resumed, restored in place, or deleted
through the ordinary workspace lifecycle.

Quarantine removes host-side network paths, mediation listeners, published TCP
listeners, and console input where they exist, and deletes the guest-facing
socket endpoints so nothing stale survives to reconnect to. New connections
fail closed.

## Freeze, sever, capture, stop

The durable marker is written first and immediately fences ordinary library
operations. The backend then freezes vCPUs before it closes broker and secret
connections, published ports, serial input, and the network datapath. The
[forensic snapshot](/cli/snapshot/#forensic-captures) is created only after
both freeze and severance are confirmed. The VM stays frozen through capture;
it is stopped only after the capture attempt completes.

The capture retains guest secrets and is **not restorable**. It is evidence, so
keep it somewhere the workloads it came from cannot read, restrict operator
access, protect backups and copies, and delete it under your evidence-retention
process. It appears in
[`snapshot list`](/cli/snapshot/) marked `retained` under `SECRETS`, and is
tagged `forensic-<timestamp>`.

If capture fails, execution and authority are not restored. The VM remains
frozen and severed, `capture` reports `failed`, and `stop` and `custody` remain
`pending`; rerun `quarantine` to retry capture against the same volatile state.
If capture cannot be recovered, rerun with `--no-capture` to explicitly accept
the evidence loss and stop the still-frozen, still-severed VM into custody.
If freeze or severance cannot be confirmed, capture is skipped and the backend
takes the emergency stop path instead. The marker remains in every failure
case.

Pass `--no-capture` to contain without capturing and accept losing the volatile
state, including when retrying after a capture failure.

Quarantine requires an audit reason and asks for confirmation while the
workspace is live. The prompt states whether evidence will be captured. Use
`--yes` only when a surrounding operator or automation workflow has already
confirmed the action. For an urgent reversible stop that must remain
frictionless, use [`halt`](/cli/halt/).

## Incident receipt

The result includes a typed `containment` object with separate `freeze`,
`severance`, `capture`, `stop`, and `custody` statuses. `status --json` returns
the same durable phase result after the original command exits. The result also
includes an `incident` receipt for the quarantined runtime session.
It identifies the runtime and session, bounds the observation window, and
summarizes lifecycle records, allowed and denied destinations, brokered byte
counts, and secret names and access outcomes. Secret values and request content
are never copied into the receipt.

`incident.complete` is `false` when any source audit stream could not be read;
`incident.errors` names those streams. Containment still proceeds when receipt
assembly is incomplete. Evidence capture failure is different: the operation
returns a typed partial result and holds the frozen, severed VM for retry.

Use `--json` to retain the complete structured receipt. Text output prints its
session and the egress, broker, and secret-access totals.

## Examples

Quarantine a workspace and inspect its custody record:

```bash
microagent quarantine research --reason "unexpected network activity"
microagent --json status research
```

## Flags

`--no-capture` is the one worth knowing; `--state-dir` matters only when the
workspace lives outside the default `~/.microagent/`.

| Flag | Description |
|---|---|
| `--no-capture` | Freeze and sever without saving a forensic snapshot; volatile evidence is lost |
| `--reason <text>` | Opaque reason recorded in the event and incident receipt as `purpose` |
| `--yes`, `-y` | Confirm without prompting; intended for deliberate automation |
| `--name <name>` | Workspace name; positional name is also accepted |
| `--id <id>` | Workspace ID alias for `--name` |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |
| `--backend <name>` | Backend identity override |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--supervisor`.

## Exit status

`quarantine` exits `0` when freeze, severance, capture (or explicit
`--no-capture`), stop, and custody complete. Capture, freeze, severance, and
final-stop failures return nonzero and leave the durable marker in place. A
capture failure leaves stop and custody pending for retry.

## Related

- [`halt`](/cli/halt/) - park a healthy workspace instead (`stop` is an alias)
- [`status`](/cli/status/) - confirm the `quarantined` state
- [State and identity](/concepts/state-and-identity/) - where `quarantined` sits in the lifecycle
