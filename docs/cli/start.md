---
title: microagent start
description: Boot a previously created workspace from its preserved disk.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

```text
microagent start <name> [--state-dir <dir>]
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

After it's running, open a console with [`connect`](/cli/connect/) on Apple
VF, Firecracker, or Windows Hyper-V, or read serial output with
[`logs`](/cli/logs/).

## Flags

Flags you'll actually use:

- `--from-snapshot <tag>` - restore memory and disk from a snapshot instead of
  booting fresh
- `--profile <name>` / `--memory <MiB>` / `--cpus <n>` - one-start resource
  overrides; the stored config is the default
- `--state-dir <dir>` - only when the workspace lives outside `~/.microagent/`

The complete set:

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory holding the workspace record (default `~/.microagent/`) |
| `--from-snapshot <tag>` | Restore the workspace in place from this snapshot tag (Firecracker) |
| `--profile <name>` | Resource profile override: `tiny`, `small`, `medium`, or `large` |
| `--memory <MiB>` | Memory override for this start |
| `--cpus <n>` | CPU count override for this start |
| `--kernel <path>` | Linux kernel path override |
| `--arch <arch>` | Guest architecture |
| `--backend <name>` | Backend identity override |
| `--vsock p=host:port` | Add a vsock mapping for this start. Repeatable |
| `--supervisor <path>` | Override the installed host backend supervisor path |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`/`--supervisor`.

## Resume in place from a snapshot

`start <name> --from-snapshot <tag>` restores the workspace in place from a
[snapshot](/cli/snapshot/) instead of booting fresh: it rolls the workspace
rootfs back to the snapshot's copy and loads the snapshot's memory and device
state, so the guest resumes exactly where it was checkpointed. This is
Firecracker-only and the snapshot's kernel must match the workspace kernel (the
load is rejected on kernel skew). Bridged networking is not supported for
restore; use user, nat, or isolated.

In-flight guest connections do not survive a restore - outbound TCP and live
vsock sessions (exec/shell/mediation) are reset and the guest process must
reconnect. Stop the workspace before restoring it in place.

`quarantined` is intentionally distinct: host-side network, mediation, and
side-effect paths were severed while the runtime may still exist. Run `halt`,
`stop`, or `kill` first, then `start` from the preserved disk state.

## Paired models

A workspace created with [`create --model`](/cli/create/) stores the model ref,
and every `start` re-pairs it: the host model runner is re-ensured (a missing
blob is auto-pulled), the workspace is registered as a holder, and the vsock
bridge plus `MICROAGENT_MODEL_URL` / `OPENAI_BASE_URL` are wired into the
guest. `halt`, `stop`, `kill`, and `delete` release the hold; a guest that
exits on its own keeps it until the next lifecycle verb, and
[`model stop`](/cli/model/) reclaims it immediately.

## Exit status

`start` exits `0` when the workspace boots; nonzero when it cannot be found,
fails to boot, or is started from an invalid state - it rejects workspaces that
are already `starting` or `running`, and refuses `quarantined` workspaces until
they are halted, stopped, or killed first. In AX mode these surface as
structured error envelopes (an invalid-state start maps to `conflict`).

## Related

- [`create`](/cli/create/) - create the workspace first
- [`stop`](/cli/stop/) - shut it down again
- [`status`](/cli/status/) - check state and readiness
- [`snapshot`](/cli/snapshot/) - manage the tags `--from-snapshot` restores
