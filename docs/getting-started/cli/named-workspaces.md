---
title: Named workspaces
description: Create, start, connect to, and delete persistent workspaces.
---

A workspace is a named, persistent VM record. Unlike `microagent run`, the
disk and state stick around between starts, so you can stop and resume an
agent's environment.

## Create

```bash
microagent create \
  --name research \
  --image docker.io/library/ubuntu:24.04 \
  --size-mib 2048 \
  --memory 1024 \
  --cpus 2 \
  --setup "mkdir -p /workspace" \
  --setup "echo ready > /workspace/status"
```

The name can also be positional:

```bash
microagent create research --image docker.io/library/ubuntu:24.04
```

`microagent` builds the rootfs once and records workspace metadata in the state
directory. If the default kernel is missing, `create` installs it first.

## Start

```bash
microagent start research
```

## Connect

```bash
microagent connect research
```

For scripts, send one line and capture new console output:

```bash
microagent connect research --send "cat /workspace/status"
```

`connect` is supported on Apple VF and Firecracker. Use [`logs`](/cli/logs/)
to review captured serial output.

## Inspect

```bash
microagent ps                       # list all workspaces
microagent status --name research   # one workspace
microagent logs research            # boot/serial output
```

## Stop and delete

```bash
microagent stop research
microagent delete research
```

For Firecracker, `delete` refuses to remove state while the recorded VM
process is still running. Use [`stop`](/cli/stop/) or [`kill`](/cli/kill/) first.

## Attach disks

Attach an existing ext4 disk:

```bash
microagent create \
  --name research \
  --image docker.io/library/ubuntu:24.04 \
  --disk workspace=/tmp/workspace.ext4:/workspace:rw
```

Build a disk from a tar bundle and mount it read-only:

```bash
microagent create \
  --name research \
  --image docker.io/library/ubuntu:24.04 \
  --bundle config=/tmp/config.tar:/config:ro
```
