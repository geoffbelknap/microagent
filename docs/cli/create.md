---
title: microagent create
description: Create a named, persistent workspace from an OCI image.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

```text
microagent create [--name <name>] --image <ref> [flags]
microagent create <name> --image <ref> [flags]
microagent create <name> --from-snapshot <workspace>:<tag> [flags]
```

`create` builds a workspace and records it under `--state-dir`. Unlike
[`run`](/cli/run/), the state survives - you can `start`, `stop`, `connect`,
and `delete` it later. If the default kernel is missing, `create` installs it
first.

## Fork from a snapshot

`create <name> --from-snapshot <workspace>:<tag>` forks a new workspace from an
existing workspace's [snapshot](/cli/snapshot/) instead of building from an
image. The fork gets a fresh identity and a private copy of the snapshot's
rootfs, then resumes from the snapshot's memory and device state.

A Firecracker snapshot binds its vsock socket to the source workspace's path, so
each fork runs Firecracker in a private mount namespace that maps the fork's own
directory over the source's, and the fork takes its own host-side service ports
while bridging them to the guest's snapshot ports. Multiple forks of the same
snapshot therefore run concurrently without colliding. This is Firecracker-only;
the snapshot kernel must match and bridged networking is unsupported. In-flight
guest connections do not survive the fork — the guest body must reconnect.

## Flags

| Flag | Description |
|---|---|
| `--image <ref>` | OCI image reference. Defaults to Python 3.13 slim |
| `--from-snapshot <workspace>:<tag>` | Fork a new workspace from an existing workspace's snapshot instead of an image (Firecracker) |
| `--file <path>` | Workspace spec file. Defaults to `microagent.yaml` or `microagent.yml` when present |
| `--name <name>` | Workspace name (also accepted as a positional argument or `--id`) |
| `--setup <command>` | Shell command to run before first start. Repeatable |
| `--setup-file <path>` | Shell script file to run before first start. Repeatable |
| `--service-command <cmd>` | Long-running shell command to run as the VM service |
| `--image-command` | Run the image Entrypoint/Cmd when creating a prepared workspace |
| `--entrypoint <command>` | Command to run on start |
| `--shell <path>` | Interactive console shell path. Defaults to `/bin/sh`; the path must exist inside the guest |
| `--hostname <name>` | Guest hostname. Defaults to the workspace name sanitized as a Linux hostname |
| `--env KEY=VALUE` | Guest environment variable. Repeatable |
| `-e KEY=VALUE` | Alias for `--env` |
| `--disk n=p:/m:ro\|rw` | Attach an existing ext4 disk |
| `--bundle n=p:/m:ro\|rw` | Build a disk from a tar bundle |
| `-v, --volume SRC:DST[:ro\|rw]` | Container-style safe volume alias for tar bundles and ext4 disk images |
| `--output n=/guest/path` | Declare an output artifact path |
| `--backend <name>` | Backend identity override |
| `--kernel <path>` | Custom kernel path |
| `--rootfs <path>` | Use an existing ext4 rootfs instead of building one from `--image`. Enables the lower-level identity flags `--id` and `--role` (see [Examples](#examples)) |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |
| `--guest-init <path>` | Guest init path |
| `--arch <arch>` | Guest architecture |
| `--profile <name>` | Resource profile: `tiny`, `small`, `medium`, or `large` |
| `--restart <policy>` | Restart policy: `never`, `on-failure`, or `always` |
| `--network <mode>` | Network mode: `user`, `nat`, `isolated`, or `bridged` |
| `--network-interface <if>` | Host interface identifier or display name for bridged mode |
| `--publish <mapping>` | Declarative TCP host port forward, `[host:]hostPort:guestPort[/tcp]`. Repeatable |
| `-p <mapping>` | Alias for `--publish` |
| `--mediation p=host:port` | Declare the guest-to-host mediation vsock channel |
| `--mediation-optional` | Allow startup when mediation is unavailable |
| `--memory <MiB>` | Memory in MiB (default 512) |
| `--cpus <n>` | CPU count |
| `--size-mib <MiB>` | Rootfs disk size |
| `--result-port <port>` | Vsock result port |
| `--mke2fs <path>` | mke2fs binary path |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--dry-run` | Validate config without creating |
| `--json <path\|->` | Read request JSON from a file or stdin; separate from the global output flag |

See [global flags](/cli/#global-flags) for `--text`/`--output`/`--mode`/`--supervisor` and the global `--json` output flag (distinct from the `--json` request-input flag above).

## Image references

`--image` accepts both digest-pinned references (`docker.io/library/ubuntu@sha256:…`) and mutable tags (`docker.io/library/ubuntu:24.04`). Both are allowed here - `create` records the resolved digest in the workspace verification record so `microagent --json status` can flag drift later. Pin by digest if you want reproducible workspaces.

[`microagent rootfs build`](/cli/rootfs/) is stricter: it rejects mutable tags unless you pass `--allow-mutable`. See [security](/security/) for the rationale.

## Examples

Create a workspace:

```bash
microagent create \
  --name research \
  --image docker.io/library/ubuntu:24.04 \
  --profile medium
```

Profiles expand to exact memory/CPU/disk configs and are stored with the workspace. See [`profiles`](/cli/profiles/) for the values.

Use `--memory`, `--cpus`, or `--size-mib` with a profile to override a single value while keeping the profile name in the workspace record.

With setup commands:

```bash
microagent create \
  --name research \
  --image docker.io/library/busybox:1.36 \
  --setup "mkdir -p /workspace" \
  --setup "echo ready > /workspace/status"
```

Use Bash for `connect`:

```bash
microagent create \
  --name research \
  --image docker.io/library/ubuntu:24.04 \
  --hostname research \
  --shell /bin/bash
```

The shell is a guest path, not a host path. If you choose a shell that is not
already in the image, install it with `--setup` or build it into the image.

Create from a declarative spec:

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

Container-style `-v` is supported for the same safe storage forms:

```bash
microagent create \
  --name research \
  --image docker.io/library/ubuntu:24.04 \
  -v /tmp/config.tar:/config:ro \
  -v /tmp/workspace.ext4:/workspace:rw
```

This does not expose host directory bind mounts or named volumes. Use a
tar archive for one-time ingress, an ext4 image for an attached disk,
`microagent cp` for stopped-workspace file transfer, and declared `--output`
paths for egress.

Lower-level form using an existing rootfs:

```bash
microagent create \
  --id agent-1 \
  --role workload \
  --kernel /tmp/kernel \
  --rootfs /tmp/rootfs.ext4 \
  --state-dir /tmp/microagent
```

The `--rootfs` path opens up two extra identity flags that the high-level
path doesn't expose:

| Flag | Description |
|---|---|
| `--id <id>` | Runtime ID for the workspace. Required on the `--rootfs` path |
| `--role <role>` | Caller-supplied role label. Defaults to `workload`. microagent records it in requests, state files, and events but does not interpret it - see [state and identity](/concepts/state-and-identity/) |

Validate without creating:

```bash
microagent create --dry-run \
  --id agent-1 \
  --kernel /tmp/kernel \
  --rootfs /tmp/rootfs.ext4 \
  --state-dir /tmp/microagent
```

Use request JSON:

```bash
microagent create --json request.json
```

For JSON output from the create command, put the global flag before the
subcommand:

```bash
microagent --json create research --image docker.io/library/ubuntu:24.04
```

## Related

- [`start`](/cli/start/), [`stop`](/cli/stop/), [`delete`](/cli/delete/)
- [State and identity](/concepts/state-and-identity/)
- [Supervisor protocol](/protocol/)
