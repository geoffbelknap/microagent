---
title: Troubleshooting
description: Common failure modes and how to diagnose them.
---

Run [`microagent doctor`](/cli/doctor/) first when something isn't working.
It reports backend availability, the binaries it found, and the default
kernel status.

## Linux / Firecracker

### `firecracker binary not found`

`microagent` looks for `firecracker` in this order:

1. `MICROAGENT_FIRECRACKER` (must be a usable file)
2. `firecracker` on `PATH`
3. `<dirname(microagent)>/../libexec/firecracker`

Either install Firecracker on PATH or set `MICROAGENT_FIRECRACKER`.

### `/dev/kvm is not available`

KVM is required for Firecracker. Check the host actually has KVM (it isn't
present in many container or sandbox environments) and that your user has
permission. Re-run the smoke **outside** sandboxed agent environments — see
[Smoke tests](/operations/smoke-tests/).

### `delete` refuses to remove a workspace

Firecracker `delete` refuses while the recorded VM process is still running.
Run [`stop`](/cli/stop/) (graceful) or [`kill`](/cli/kill/) (forceful) first.

### Kernel SHA mismatch

The default Firecracker amd64 kernel SHA is
`4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0`. Verify
with [`microagent kernel verify`](/cli/kernel/). Reinstall with
`microagent kernel install` if the file is corrupted.

## macOS / Apple VF

### Supervisor not found

Override with `--supervisor <path>` or set
`MICROAGENT_APPLEVF_SUPERVISOR`. From a source checkout:

```bash
microagent create \
  --backend apple-vf \
  --supervisor ./supervisors/applevf/.build/debug/microagent-applevf-supervisor \
  --json request.json
```

Here `create --json <path>` reads request JSON. For structured command output,
place the global output flag before the subcommand.

Use `make signed-supervisor` to produce an ad-hoc-signed build.

### Default kernel missing

`microagent` looks for the arm64 kernel at
`~/.microagent/kernels/apple-vf/arm64/Image` (the older
`~/.microagent/kernels/apple-vf/Image` still works). Install with
`microagent kernel install`.

## General

### Need structured output

Every command supports the global `--json` flag before the subcommand (or
`MICROAGENT_OUTPUT=json`). Scripts should
consume JSON; the [supervisor protocol](/protocol/) describes the shape.
