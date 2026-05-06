---
title: microagent run
description: Boot a VM from an OCI image, run a command, and tear down.
---

```text
microagent run --image <ref> --exec "<command>" [flags]
```

`run` is the one-shot path. Microagent fetches the image, builds a rootfs,
boots the VM, runs `--setup` then `--exec`, prints the result, and removes
scratch state (unless `--keep` is set).

## Flags

| Flag | Description |
|---|---|
| `--image <ref>` | OCI image reference |
| `--exec <command>` | Shell command to run |
| `--setup <command>` | Shell command to run before `--exec`. Repeatable |
| `--entrypoint <command>` | Command to run on start |
| `--env KEY=VALUE` | Guest environment variable. Repeatable |
| `--disk n=p:/m:ro\|rw` | Attach an existing ext4 disk |
| `--bundle n=p:/m:ro\|rw` | Build a disk from a tar bundle |
| `--name <name>` | Workspace name; generated when omitted |
| `--kernel <path>` | Custom kernel path |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |
| `--memory <MiB>` | Memory in MiB (default 512) |
| `--cpus <n>` | CPU count |
| `--size-mib <MiB>` | Rootfs disk size |
| `--timeout <seconds>` | Maximum wall-clock time before kill |
| `--keep` | Keep state after the command exits |
| `--mke2fs <path>` | mke2fs binary path |
| `--supervisor <path>` | Override the active backend supervisor path |

## Examples

Run a single command:

```bash
microagent run \
  --image docker.io/library/ubuntu:24.04 \
  --exec "uname -a"
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
