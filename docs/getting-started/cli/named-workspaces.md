---
title: Named workspaces
description: Create, start, connect to, and delete persistent workspaces.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

A workspace is a named, persistent VM record. Unlike `microagent run`, the
disk and state stick around between starts, so you can stop and resume an
agent's environment.

## Create

```bash
microagent create \
  --name research \
  --image docker.io/library/ubuntu:24.04 \
  --profile medium \
  --setup "mkdir -p /workspace" \
  --setup "echo ready > /workspace/status"
```

`--profile` picks a named resource size (such as `medium`); it's the
recommended way to size a workspace. To set resources directly, override with
`--memory`, `--cpus`, and `--size-mib` instead of (or on top of) a profile.

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

`connect` is supported by Apple VF, Firecracker, and experimental
Windows-HyperV. Use [`logs`](/cli/logs/) to review captured serial output.

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

## What's next

- **Run an actual agent in a persistent workspace** - see [run your first agent](/getting-started/cli/first-agent/).
- **Describe a whole workspace in one file** - see the [`microagent.yaml`](/cli/spec/) spec reference.
- **Drive workspaces from Go instead of the CLI** - start with the [library overview](/library/).
