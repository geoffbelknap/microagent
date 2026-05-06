---
title: microagent create
description: Create a named, persistent workspace from an OCI image.
---

```text
microagent create [--name <name>] --image <ref> [flags]
microagent create <name> --image <ref> [flags]
```

`create` builds a workspace and records it under `--state-dir`. Unlike
[`run`](/cli/run/), the state survives — you can `start`, `stop`, `connect`,
and `delete` it later. If the default kernel is missing, `create` installs it
first.

## Flags

| Flag | Description |
|---|---|
| `--image <ref>` | OCI image reference. Defaults to a small BusyBox image |
| `--name <name>` | Workspace name (also accepted as a positional argument) |
| `--setup <command>` | Shell command to run before first start. Repeatable |
| `--entrypoint <command>` | Command to run on start |
| `--env KEY=VALUE` | Guest environment variable. Repeatable |
| `--disk n=p:/m:ro\|rw` | Attach an existing ext4 disk |
| `--bundle n=p:/m:ro\|rw` | Build a disk from a tar bundle |
| `--kernel <path>` | Custom kernel path |
| `--state-dir <dir>` | State directory |
| `--profile <name>` | Resource profile: `tiny`, `small`, `medium`, or `large` |
| `--memory <MiB>` | Memory in MiB (default 512) |
| `--cpus <n>` | CPU count |
| `--size-mib <MiB>` | Rootfs disk size |
| `--mke2fs <path>` | mke2fs binary path |
| `--supervisor <path>` | Override the active backend supervisor path |
| `--dry-run` | Validate config without creating |
| `--json <path\|->` | Read a [request JSON](/protocol/applevf/) from a file or stdin |

## Examples

Create a workspace:

```bash
microagent create \
  --name research \
  --image docker.io/library/ubuntu:24.04 \
  --profile medium
```

Profiles expand to exact configs and are stored with the workspace:

| Profile | Memory MiB | CPUs | Disk MiB |
|---|---:|---:|---:|
| `tiny` | 256 | 1 | 512 |
| `small` | 512 | 2 | 1024 |
| `medium` | 2048 | 2 | 8192 |
| `large` | 4096 | 4 | 16384 |

Use `--memory`, `--cpus`, or `--size-mib` with a profile to override a single
value while keeping the profile name in the workspace record.

With setup commands:

```bash
microagent create \
  --name research \
  --image docker.io/library/busybox:1.36 \
  --setup "mkdir -p /workspace" \
  --setup "echo ready > /workspace/status"
```

Attach an existing ext4 disk:

```bash
microagent create \
  --name research \
  --image docker.io/library/ubuntu:24.04 \
  --disk workspace=/tmp/workspace.ext4:/workspace:rw
```

Build a disk from a tar bundle, mounted read-only:

```bash
microagent create \
  --name research \
  --image docker.io/library/ubuntu:24.04 \
  --bundle config=/tmp/config.tar:/config:ro
```

Lower-level form using an existing rootfs:

```bash
microagent create \
  --id agent-1 \
  --kernel /tmp/kernel \
  --rootfs /tmp/rootfs.ext4 \
  --state-dir /tmp/microagent-kit
```

Validate without creating:

```bash
microagent create --dry-run \
  --id agent-1 \
  --kernel /tmp/kernel \
  --rootfs /tmp/rootfs.ext4 \
  --state-dir /tmp/microagent-kit
```

Use request JSON:

```bash
microagent create --json request.json
```

## Related

- [`start`](/cli/start/), [`stop`](/cli/stop/), [`delete`](/cli/delete/)
- [State and identity](/concepts/state-and-identity/)
- [Supervisor contract](/protocol/)
