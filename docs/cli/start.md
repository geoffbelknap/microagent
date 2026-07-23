---
title: microagent start
description: Boot a previously created workspace from its preserved disk.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-23_

```text
microagent start <name> [--state-dir <dir>]
microagent start <name> --wait [--wait-timeout <dur>]
microagent start <name> --from-snapshot <tag> [--state-dir <dir>]
```

`start` boots a workspace that was previously created. The workspace must
exist in the state directory (default `~/.microagent/`).

Start is disk-state resume, not memory resume. It boots from the persisted
workspace disk after `prepared`, `halted`, `stopped`, or `failed`. It rejects
workspaces that are already `starting` or `running`. To thaw a `paused`
workspace, use [`resume`](/cli/resume/) instead.

## Examples

Boot a created or halted workspace:

```bash
microagent start research
```

`start` reuses the resource config stored by `create`. Pass `--profile`,
`--memory`, or `--cpus` only when you want a one-start override:

```bash
microagent start research --profile large
```

Resume in place from a snapshot:

```bash
microagent start research --from-snapshot pre-upgrade
```

After it's running, open a console with [`connect`](/cli/connect/) or read
serial output with [`logs`](/cli/logs/).

`start` returns once the VM boots, not when the workload finishes. When the
workspace runs something that ends on its own - an agent, a batch job - add
`--wait` to block until it reaches a terminal state:

```bash
microagent start minimal-agent --wait
microagent --json result minimal-agent
```

With `--wait`, the boot result is written first and a
[`wait`](/cli/wait/)-shaped result follows when the run finishes (with the
global `--json` flag that means two JSON documents on one stream; decode it
as a stream, or run [`wait`](/cli/wait/) as its own command for a single
document). Under `--mode ax`, only the final wait-outcome envelope is
written - the intermediate boot envelope is suppressed so the AX stream stays
one document, as it is for every other AX response. The exit code is `0` for
`stopped`/`halted` and `1` for `failed`/`quarantined`, exactly like
`microagent wait`.

## Flags

Common flags:

- `--wait` - after boot, block until the workspace reaches a terminal state
  (`--wait-timeout <dur>` bounds it and implies `--wait`)
- `--from-snapshot <tag>` - restore memory and disk from a snapshot instead of
  booting fresh
- `--profile <name>` / `--memory <MiB>` / `--cpus <n>` - one-start resource
  overrides; the stored config is the default
- `--ttl <seconds>` - idle TTL; the VM is reaped after this long with no
  `exec`/`connect` activity (activity renews it). `0` (default) means permanent.
  Preserves a lease declared at `create` time
- `--state-dir <dir>` - only when the workspace lives outside `~/.microagent/`

The complete set:

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |
| `--wait` | After boot, block until the workspace reaches a terminal state (stopped, halted, failed) |
| `--wait-timeout <dur>` | Give up waiting after this long (e.g. `5m`); `0` waits forever; implies `--wait` |
| `--from-snapshot <tag>` | Restore the workspace in place from this snapshot tag |
| `--profile <name>` | Resource profile override: `tiny`, `small`, `medium`, or `large` |
| `--memory <MiB>` | Memory override for this start |
| `--cpus <n>` | CPU count override for this start |
| `--ttl <seconds>` | Idle TTL; the VM is reaped after this long with no exec/connect activity. `0` = permanent (preserves a create-time lease) |
| `--kernel <path>` | Linux kernel path override |
| `--arch <arch>` | Guest architecture |
| `--backend <name>` | Backend identity override |
| `--vsock p=host:port` | Add a vsock mapping for this start. Repeatable |
| `--model-runner <backend>` | Model runner backend override for this start: `llamacpp`, `vllm`, or `custom` |
| `--model-gpu <mode>` | Model runner GPU intent override: `off`, `on`, or `auto` |
| `--model-runner-model <id>` | Backend model id override for runners such as vLLM |
| `--model-runner-served-model <name>` | OpenAI-compatible served model name override for runners such as vLLM |
| `--model-runner-command <template>` | Custom model runner command override for this start |
| `--model-runner-name <name>` | Custom host model runner name override |
| `--model-runner-health-path <path>` | Custom host model runner health probe path override |
| `--model-runner-arg <arg>` | Extra model runner argument override. Repeatable |
| `--model-runner-env KEY=VALUE` | Extra model runner environment for this start. Repeatable; values are not persisted |
| `--model-mediation <mode>` | Model mediation mode override: `off`, `local-allow`, or `policy` |
| `--model-policy-file <path>` | Structured model mediation policy file override |
| `--model-policy-url <url>` | External model mediation policy endpoint override |
| `--model-policy-timeout <duration>` | Model mediation policy timeout override |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--mode`/`--supervisor`.

## Resume in place from a snapshot

`start <name> --from-snapshot <tag>` restores the workspace in place from a
[snapshot](/cli/snapshot/) instead of booting fresh: it rolls the workspace
rootfs back to the snapshot's copy and loads the snapshot's memory and device
state, so the guest resumes exactly where it was checkpointed. The snapshot's
kernel must match the workspace kernel; the load is rejected on kernel skew.

In-flight guest connections do not survive a restore - outbound TCP and live
vsock sessions (exec/shell/mediation) are reset and the guest process must
reconnect. Stop the workspace before restoring it in place.

`quarantined` is intentionally distinct: host-side network, mediation, and
side-effect paths were severed while the runtime may still exist. Run `halt`,
`stop`, or `kill` first, then `start` from the preserved disk state.

## Paired models

A workspace created with [`create --model`](/cli/create/) stores the model ref,
model runner config, and model mediation config. Every `start` re-pairs it:
the host model runner is re-ensured (a missing blob is auto-pulled), the
workspace is registered as a holder, and the vsock bridge plus
`MICROAGENT_MODEL_URL` / `OPENAI_BASE_URL` are wired into the guest. The
`--model-runner*` and `--model-mediation*` flags override the stored pairing
for one boot. `halt`, `stop`, `kill`, and `delete` release the hold; a guest
that exits on its own keeps it until the next lifecycle verb, and
[`model stop`](/cli/model/) reclaims it immediately. Attach and release
actions are recorded in the workspace [`events`](/cli/events/) history as
`model_worker=attached` and `model_worker=released` markers.

## Exit status

`start` exits `0` when the workspace boots; nonzero when it cannot be found,
fails to boot, or is started from an invalid state - it rejects workspaces that
are already `starting` or `running`, and refuses `quarantined` workspaces until
they are halted, stopped, or killed first. In AX mode these return structured
error envelopes (an invalid-state start maps to `conflict`). With `--wait`,
the exit code additionally follows the final state like
[`wait`](/cli/wait/#exit-status): `0` for `stopped`/`halted`, `1` for
`failed`/`quarantined`, nonzero on `--wait-timeout`.

## Related

- [`create`](/cli/create/) - create the workspace first
- [`wait`](/cli/wait/) - block until an already-started run finishes
- [`halt`](/cli/halt/) - shut it down again (`stop` is an alias)
- [`status`](/cli/status/) - check state and readiness
- [`snapshot`](/cli/snapshot/) - manage the tags `--from-snapshot` restores
