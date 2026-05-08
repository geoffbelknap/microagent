---
title: Run your first agent workspace
description: Use `microagent run` for a one-shot task inside a Linux workspace.
---

*Driving microagent from a Go program instead? See the [library quickstart](/getting-started/library/first-program/).*

Start with `microagent run`. It pulls an OCI image, builds a Linux workspace,
boots it with the host backend, runs your command, and removes the scratch state
afterward.

Microagent does not plan, call an LLM, grant credentials, or decide policy. Your
agent runtime owns those parts. Microagent gives it a clean place to run work.

```bash
microagent run \
  --image docker.io/library/ubuntu:24.04 \
  --setup "mkdir -p /workspace" \
  --exec "printf 'agent workspace ready\n'; uname -a"
```

Microagent downloads the default kernel for the host backend the first time it
needs one.

## Prepare the workspace first

Use `--setup` for files or directories the task needs. Repeat the flag for
multiple setup commands.

```bash
microagent run \
  --image docker.io/library/busybox:1.36 \
  --setup "mkdir -p /workspace" \
  --setup "echo 'summarize /workspace/input.txt' > /workspace/task" \
  --setup "echo 'hello from an isolated workspace' > /workspace/input.txt" \
  --exec "cat /workspace/task; cat /workspace/input.txt"
```

## Keep agent identity explicit

For one-shot runs, pass caller-visible identity with `--id` and `--role`. Use
`workload` for an agent workspace unless you are starting an enforcement
component. Microagent records that identity in requests, state files, and events,
but it does not interpret user intent.

```bash
microagent run \
  --id agent-1 \
  --role workload \
  --image docker.io/library/ubuntu:24.04 \
  --exec "cat /etc/os-release"
```

Use that identity in your own runtime to connect VM state back to the agent,
principal, task, or audit record. See
[State and identity](/concepts/state-and-identity/) for the files Microagent
writes.

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
4. The guest init ran `--setup`, then ran your task command.
5. Microagent wrote the result state, shut the VM down, and removed the scratch
   state.

To keep an agent workspace around for later `start`, `connect`, artifact
inspection, and shutdown, see
[Named workspaces](/getting-started/cli/named-workspaces/) and [`create`](/cli/create/).
