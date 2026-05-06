---
title: microagent.yaml
description: Declarative workspace spec for reproducible creates.
---

`microagent.yaml` records the inputs needed to recreate a workspace from source
control. It is consumed by [`microagent create`](/cli/create/).

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
disks:
  - name: workspace
    path: /tmp/workspace.ext4
    mountpoint: /workspace
    mode: rw
bundles:
  - name: config
    path: ./config.tar
    mountpoint: /config
    mode: ro
```

## Usage

```bash
microagent create --file microagent.yaml
```

If `microagent.yaml` or `microagent.yml` exists in the current directory,
`microagent create` reads it automatically.

CLI flags override spec fields, so this is valid:

```bash
microagent create --file microagent.yaml --name research-2 --profile large
```

## Fields

| Field | Description |
|---|---|
| `name` | Workspace name |
| `image` | OCI image reference |
| `profile` | Resource profile: `tiny`, `small`, `medium`, or `large` |
| `restart` | Restart policy: `never`, `on-failure`, or `always` |
| `entrypoint` | Command to run when the workspace starts |
| `setup` | Commands to run before first start |
| `env` | Guest environment variables |
| `resources.memoryMiB` | Memory override |
| `resources.cpuCount` | CPU override |
| `resources.sizeMiB` | Rootfs disk size override |
| `disks` | Existing ext4 disks to attach |
| `bundles` | Tar bundles to build into ext4 disks and attach |

## Related

- [`create`](/cli/create/)
- [`profiles`](/cli/)
- [`cp`](/cli/cp/)
