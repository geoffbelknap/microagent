---
title: microagent run
description: Boot a VM from an OCI image, run a command, and tear down.
---

```text
microagent run --image <ref> --exec "<command>" [flags]
```

`run` is the one-shot path. It fetches the image, builds a rootfs, boots the
VM, runs `--setup` then `--exec`, prints the result, and removes scratch state
(unless `--keep` is set).

## Flags

| Flag | Description |
|---|---|
| `--image <ref>` | OCI image reference |
| `--exec <command>` | Shell command to run |
| `--setup <command>` | Shell command to run before `--exec`. Repeatable |
| `--entrypoint <command>` | Command to run on start |
| `--shell <path>` | Interactive console shell path for kept/named runs. Defaults to `/bin/sh` |
| `--hostname <name>` | Guest hostname. Defaults to the workspace name sanitized as a Linux hostname |
| `--env KEY=VALUE` | Guest environment variable. Repeatable |
| `--disk n=p:/m:ro\|rw` | Attach an existing ext4 disk |
| `--bundle n=p:/m:ro\|rw` | Build a disk from a tar bundle |
| `--output n=/guest/path` | Declare an output artifact path |
| `--name <name>` | Workspace name; generated when omitted. Also accepted as `--id` |
| `--kernel <path>` | Custom kernel path |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |
| `--profile <name>` | Resource profile: `tiny`, `small`, `medium`, or `large` |
| `--mediation p=host:port` | Declare the guest-to-host mediation vsock channel |
| `--mediation-optional` | Allow startup when mediation is unavailable |
| `--memory <MiB>` | Memory in MiB (default 512) |
| `--cpus <n>` | CPU count |
| `--size-mib <MiB>` | Rootfs disk size |
| `--timeout <seconds>` | Maximum wall-clock time before kill |
| `--keep` | Keep state after the command exits |
| `--mke2fs <path>` | mke2fs binary path |
| `--supervisor <path>` | Override the installed host backend supervisor path |

## Image references

`--image` accepts both digest-pinned references (`docker.io/library/ubuntu@sha256:…`) and mutable tags. Both are allowed here. For repeatable runs in CI or production, pin by digest. [`microagent rootfs build`](/cli/rootfs/) is the stricter path — it rejects mutable tags unless you pass `--allow-mutable`. See [security](/security/) for the rationale.

## Examples

Run a single command:

```bash
microagent run \
  --image docker.io/library/ubuntu:24.04 \
  --exec "uname -a"
```

Run with a named resource profile:

```bash
microagent run \
  --image docker.io/library/ubuntu:24.04 \
  --profile medium \
  --exec "apt-get update"
```

Run setup commands first:

```bash
microagent run \
  --image docker.io/library/busybox:1.36 \
  --setup "mkdir -p /workspace" \
  --setup "echo ready > /workspace/status" \
  --exec "cat /workspace/status"
```

Use a custom kernel:

```bash
microagent run \
  --image docker.io/library/ubuntu:24.04 \
  --exec "uname -a" \
  --kernel /tmp/Image
```

## Related

- [`create`](/cli/create/) — keep the workspace between starts
- [`kernel install`](/cli/kernel/) — manage kernels explicitly
- [`rootfs build`](/cli/rootfs/) — build a rootfs without booting
