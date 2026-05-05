---
title: Run your first VM
description: Use `microagent run` to boot an OCI image and execute a command.
---

The fastest path to a running microVM is `microagent run`. It pulls an OCI
image, builds a rootfs, boots a VM, runs the command you supply, and tears
down.

```bash
microagent run \
  --image docker.io/library/ubuntu:24.04 \
  --exec "uname -a"
```

Microagent downloads the default kernel for the host backend the first time it
needs one.

## Run setup commands first

`--setup` runs once before `--exec`. Repeat the flag for multiple commands.

```bash
microagent run \
  --image docker.io/library/busybox:1.36 \
  --setup "mkdir -p /workspace" \
  --setup "echo ready > /workspace/status" \
  --exec "cat /workspace/status"
```

## Use a custom kernel

```bash
microagent run \
  --image docker.io/library/ubuntu:24.04 \
  --exec "uname -a" \
  --kernel /tmp/Image
```

Manage kernels explicitly with [`microagent kernel`](/cli/kernel/).

## What just happened

1. Microagent fetched the OCI image.
2. It converted the image into an ext4 rootfs.
3. It booted the VM via the host backend ([Firecracker or Apple VF](/concepts/backends/)).
4. The guest init ran `--setup` then `--exec`.
5. Microagent collected output, shut the VM down, and removed scratch state.

To keep the workspace around so you can `start` and `stop` it on demand, see
[Named workspaces](/getting-started/named-workspaces/) and [`create`](/cli/create/).
