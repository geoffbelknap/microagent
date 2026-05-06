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
| `--file <path>` | Workspace spec file. Defaults to `microagent.yaml` or `microagent.yml` when present |
| `--name <name>` | Workspace name (also accepted as a positional argument) |
| `--setup <command>` | Shell command to run before first start. Repeatable |
| `--entrypoint <command>` | Command to run on start |
| `--env KEY=VALUE` | Guest environment variable. Repeatable |
| `--disk n=p:/m:ro\|rw` | Attach an existing ext4 disk |
| `--bundle n=p:/m:ro\|rw` | Build a disk from a tar bundle |
| `--output n=/guest/path` | Declare an output artifact path |
| `--kernel <path>` | Custom kernel path |
| `--state-dir <dir>` | State directory |
| `--profile <name>` | Resource profile: `tiny`, `small`, `medium`, or `large` |
| `--restart <policy>` | Restart policy: `never`, `on-failure`, or `always` |
| `--network <mode>` | Network mode: `nat`, `isolated`, or `bridged` |
| `--network-interface <if>` | Host interface identifier or display name for bridged mode |
| `--publish <mapping>` | Declarative TCP host port forward, `[host:]hostPort:guestPort[/tcp]`. Repeatable |
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

Create from a declarative spec:

```yaml
name: research
image: docker.io/library/ubuntu:24.04
profile: medium
restart: on-failure
entrypoint: /app/start.sh
setup:
  - mkdir -p /workspace
  - echo ready > /workspace/status
env:
  MICROAGENT_NAME: research
resources:
  memoryMiB: 2048
  cpuCount: 2
  sizeMiB: 8192
network:
  mode: nat
  forwards:
    - host: 127.0.0.1
      hostPort: 8080
      guestPort: 80
      protocol: tcp
disks:
  - name: workspace
    path: /tmp/workspace.ext4
    mountpoint: /workspace
    mode: rw
```

```bash
microagent create --file microagent.yaml
```

When `microagent.yaml` or `microagent.yml` exists in the current directory,
`microagent create` reads it automatically. CLI flags override fields from the
spec.

Restart policies are enforced by [`supervise`](/cli/supervise/).

On Apple VF, `bridged` also requires a supervisor signed with Apple's
restricted `com.apple.vm.networking` entitlement. Open-source builds cannot
self-sign that entitlement, and `sudo` does not bypass the check. Local ad-hoc
supervisors fail closed with a clear error.

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
- [Supervisor protocol](/protocol/)
