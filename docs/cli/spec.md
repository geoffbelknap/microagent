---
title: microagent.yaml
description: Declarative workspace spec for reproducible creates.
---

`microagent.yaml` records the inputs needed to recreate a workspace from source
control. It is consumed by [`microagent create`](create.md).

```yaml
name: research
image: docker.io/library/ubuntu:24.04
profile: medium
restart: on-failure
entrypoint: /app/start.sh
setup:
  - mkdir -p /workspace
  - echo ready > /workspace/status
files:
  - src: ./body.py
    dst: /app/body.py
    mode: "0644"
env:
  MICROAGENT_NAME: research
resources:
  memoryMiB: 2048
  cpuCount: 2
  sizeMiB: 8192
mediation:
  enabled: true
  required: true
  port: 2048
  target: 127.0.0.1:9900
  failClosed: true
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
outputs:
  - name: report
    path: /workspace/report.json
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
| `files` | Source files to copy into the workspace rootfs |
| `files[].src` | Host path, relative to the spec file or absolute |
| `files[].dst` | Absolute guest path to write |
| `files[].mode` | Optional octal file mode string, such as `"0755"` |
| `env` | Guest environment variables |
| `resources.memoryMiB` | Memory override |
| `resources.cpuCount` | CPU override |
| `resources.sizeMiB` | Rootfs disk size override |
| `mediation` | Guest-to-host vsock mediation channel contract |
| `mediation.enabled` | Enables the mediation declaration |
| `mediation.required` | Requires the channel for workspace startup |
| `mediation.port` | Guest vsock port used by the Body |
| `mediation.target` | Host address and port for the enforcer/orchestrator |
| `mediation.failClosed` | Treats a required channel break as closed by default |
| `disks` | Existing ext4 disks to attach |
| `bundles` | Tar bundles to build into ext4 disks and attach |
| `outputs` | Declared output artifact paths inside the workspace |

## Related

- [`create`](create.md)
- [`profiles`](profiles.md)
- [`cp`](cp.md)
