---
title: Workspace spec
description: Declarative microagent.yaml format for reproducible creates.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

`microagent.yaml` records the inputs needed to recreate a workspace from source
control. It is the declarative form of [`microagent create`](/cli/create/):
each field corresponds to a `create` flag, and when both are given, CLI flags
override matching spec fields. Use the file when the workspace definition
should live in the repo; use flags for one-off overrides.

```yaml
name: research
image: docker.io/library/ubuntu:24.04
profile: medium
restart: on-failure
entrypoint: /app/start.sh
shell: /bin/bash
hostname: research
setup:
  - mkdir -p /workspace
  - echo ready > /workspace/status
files:
  - src: ./agent.py
    dst: /app/agent.py
    mode: "0644"
env:
  MICROAGENT_NAME: research
model: unsloth/Qwen3-4B-Instruct-2507-GGUF/Qwen3-4B-Instruct-2507-Q4_K_M.gguf
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
health:
  exec: ["python3", "-c", "import socket; socket.create_connection(('127.0.0.1', 8080), 1)"]
  intervalSeconds: 15
  timeoutSeconds: 2
  retries: 3
  startPeriodSeconds: 10
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
| `shell` | Interactive console shell path. Defaults to `/bin/sh`; the path must exist inside the guest |
| `hostname` | Guest hostname. Defaults to the workspace name sanitized as a Linux hostname |
| `setup` | Commands to run before first start |
| `files` | Source files to copy into the workspace rootfs |
| `files[].src` | Host path, relative to the spec file or absolute |
| `files[].dst` | Absolute guest path to write |
| `files[].mode` | Optional octal file mode string, such as `"0755"` |
| `env` | Guest environment variables |
| `model` | HuggingFace GGUF ref of a locally served model to pair the workspace with; every `start` re-pairs it, and a CLI `--model` flag overrides the field. See [`model`](/cli/model/) |
| `resources.memoryMiB` | Memory override |
| `resources.cpuCount` | CPU override |
| `resources.sizeMiB` | Rootfs disk size override |
| `mediation` | Guest-to-host vsock mediation channel contract |
| `mediation.enabled` | Enables the mediation declaration |
| `mediation.required` | Requires the channel for workspace startup |
| `mediation.port` | Guest vsock port used by the agent |
| `mediation.target` | Host address and port for the enforcer/orchestrator |
| `mediation.failClosed` | Treats a required channel break as closed by default |
| `health` | Liveness probe; an unhealthy workspace is restarted by [`supervise`](/cli/supervise/) under the restart policy |
| `health.exec` | Probe command run in the guest through structured exec when the selected backend exposes `execReady`; healthy on exit 0. Declare either `exec` or `httpGet` |
| `health.httpGet` | Probe path for a host-side GET against a published guest port (e.g. `/healthz`); healthy on a non-error status |
| `health.port` | Published guest port the `httpGet` probe targets |
| `health.intervalSeconds` | Seconds between probes (default 30) |
| `health.timeoutSeconds` | Per-probe timeout (default 5) |
| `health.retries` | Consecutive failures before the workspace is considered unhealthy (default 3) |
| `health.startPeriodSeconds` | Grace period after start before probing begins (default 0) |
| `disks` | Existing ext4 disks to attach |
| `bundles` | Tar bundles to build into ext4 disks and attach |
| `outputs` | Declared output artifact paths inside the workspace |

## Related

- [`create`](/cli/create/) - the command this file drives
- [`apply`](/cli/apply/) - apply spec changes to an existing workspace
- [`profiles`](/cli/profiles/) - the named resource profiles
- [`supervise`](/cli/supervise/) - acts on `restart` and `health`
