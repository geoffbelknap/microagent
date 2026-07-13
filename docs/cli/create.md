---
title: microagent create
description: Create a named workspace that survives between starts.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-13_

```text
microagent create [--name <name>] [--image <ref>] [flags]
microagent create <name> [--image <ref>] [flags]
microagent create <name> --from-snapshot <workspace>:<tag> [flags]
```

`create` builds a workspace and records it under `--state-dir`. Unlike
[`run`](/cli/run/), the state survives - you can `start`, `stop`, `connect`,
and `delete` it later. Use `run` for disposable one-shot work; use `create`
when you'll come back to the same disk. If the default kernel is missing,
`create` installs it first.

## Examples

Create a workspace, then boot it:

```bash
microagent create \
  --name research \
  --image docker.io/library/ubuntu:24.04 \
  --profile medium
microagent start research
```

Profiles expand to exact memory/CPU/disk configs and are stored with the
workspace. See [`profiles`](/cli/profiles/) for the values. Use `--memory`,
`--cpus`, or `--size-mib` with a profile to override a single value while
keeping the profile name in the workspace record.

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
  mode: user
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

Pair the workspace with a locally served model:

```bash
microagent create \
  --name research \
  --image docker.io/library/python:3.13-slim \
  --model unsloth/Qwen3-4B-Instruct-2507-GGUF/Qwen3-4B-Instruct-2507-Q4_K_M.gguf
```

The ref is stored with the workspace, so every [`start`](/cli/start/) re-pairs
it - the host model server is re-ensured and `OPENAI_BASE_URL` points the guest
at it. See [`model`](/cli/model/) for serving and release semantics.

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

Attach a [named volume](/cli/volume/) by name with `-v data:/work` for
persistent, VM-independent storage. Host directory bind mounts are not exposed:
use a tar archive for one-time ingress, an ext4 image for an attached disk,
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
microagent create --request-json request.json
```

For JSON *output*, use the global flag before the subcommand:

```bash
microagent --json create research --image docker.io/library/ubuntu:24.04
```

## Flags

Common flags:

- `--name <name>` - the name everything else refers to; positional works too
- `--image <ref>` - the OCI image the workspace boots from
- `--profile <name>` - size the VM (`tiny`, `small`, `medium`, `large`)
- `--setup <command>` - bake first-boot prep into the workspace; repeatable
- `--file <path>` - create from a declarative `microagent.yaml` spec
- `-v SRC:DST[:ro|rw]` - attach a named volume, tar bundle, or ext4 disk
- `--model <ref>` - pair the workspace with a locally served model; every
  `start` re-pairs it
- `--restart <policy>` - what [`supervise`](/cli/supervise/) does when it exits
- `--dry-run` - validate the config without creating anything

The rest, grouped:

### Workspace basics

| Flag | Description |
|---|---|
| `--image <ref>` | OCI image reference. Defaults to Python 3.13 slim when omitted |
| `--from-snapshot <workspace>:<tag>` | Fork from an existing workspace's snapshot instead of an image |
| `--file <path>` | Workspace spec file. Defaults to `microagent.yaml` / `microagent.yml` when present |
| `--name <name>` | Workspace name (also positional, or `--id`) |
| `--setup <command>` | Shell command to run before first start. Repeatable |
| `--setup-file <path>` | Shell script file to run before first start. Repeatable |
| `--service-command <cmd>` | Long-running shell command to run as the VM service |
| `--image-command` | Run the image Entrypoint/Cmd as the service |
| `--entrypoint <command>` | Command to run on start |
| `--shell <path>` | Console shell path. Defaults to `/bin/sh`; must exist in the guest |
| `--hostname <name>` | Guest hostname. Defaults to the sanitized workspace name |
| `--env KEY=VALUE`, `-e` | Guest environment variable. Repeatable |
| `--restart <policy>` | `never`, `on-failure`, or `always`. Enforced by [`supervise`](/cli/supervise/) |
| `--ttl <seconds>` | Idle lease: reap the VM after this long with no `exec`/`connect`. `0` = permanent |
| `--timeout <seconds>` | Run timeout in seconds; must be positive |
| `--dry-run` | Validate config without creating |

The image default is digest-pinned for `arm64`/`amd64` (the `python:3.13-slim`
tag elsewhere); the `--rootfs` and `--from-snapshot` paths take no image.
Activity on the workspace renews the `--ttl` lease, so it only reaps VMs that
have gone quiet.

### Resources & networking

| Flag | Description |
|---|---|
| `--profile <name>` | Resource profile: `tiny`, `small`, `medium`, or `large` |
| `--memory <MiB>` | Memory in MiB (default 512) |
| `--cpus <n>` | CPU count |
| `--size-mib <MiB>` | Rootfs disk size (default: grows to fit the image) |
| `--network <mode>` | Network mode: `user` (default) or `isolated` |
| `--publish <mapping>`, `-p` | Forward `[host:]hostPort:guestPort[/tcp]`. Repeatable |

### Files, disks & volumes

| Flag | Description |
|---|---|
| `--disk n=p:/m:ro\|rw` | Attach an existing ext4 disk |
| `--bundle n=p:/m:ro\|rw` | Build a disk from a tar bundle |
| `-v, --volume SRC:DST[:ro\|rw]` | Attach a named volume, tar bundle, or ext4 disk image |
| `--output n=/guest/path` | Declare an output artifact path |

### Secrets & credentials

| Flag | Description |
|---|---|
| `--secret NAME=<scheme>:<ref>` | Deliver a secret to `/run/secrets/NAME`, re-resolved each start. Repeatable. See [`secret`](/cli/secret/) |
| `--secrets-env-file <path>` | Deliver every key in a dotenv file as a secret |
| `--secret-on-demand NAME=<scheme>:<ref>` | Secret fetched at runtime via `$MICROAGENT_SECRETS_SOCK`, never written to tmpfs. Repeatable |
| `--secrets-audit` | Log every secret access (`microagent secret audit`) |

### Egress & broker

| Flag | Description |
|---|---|
| `--egress <mode>` | `broker` (default), `mitm`, or `off`. Persisted with the workspace |
| `--egress-lock-allowlist` | Only allowlisted hosts are reachable. Works in `broker` or `mitm` |
| `--egress-allow <host>` | Allowlist a destination: exact host or `.suffix`. Repeatable |
| `--egress-passthrough <host>` | Allowed host forwarded opaquely, never TLS-intercepted (for cert-pinned/mTLS endpoints). Repeatable |
| `--egress-policy <path>` | Policy file declaring `allow[]`/`passthrough[]`; unioned with the flags. Requires `--egress broker` or `mitm` |
| `--egress-swap-config <path>` | Credential-swap config (YAML). Requires `--egress mitm` |
| `--cred-swap PROVIDER[=ref]` | Swap in a built-in provider's API key host-side. Repeatable; requires `--egress mitm` |
| `--broker-upstream <url>` | Egress broker upstream base URL. Persisted with the workspace |
| `--broker-secret NAME=<scheme>:<ref>` | Broker credential reference. Required with `--broker-upstream` |
| `--broker-env KEY[=VALUE]` | Guest env var pointed at the broker; empty `VALUE` = broker URL. Repeatable |
| `--broker-proxy` | Also set `HTTPS_PROXY`/`HTTP_PROXY` in the guest to the broker |
| `--broker-capture` | Opt in to raw capture of pre-swap broker requests (off by default) |
| `--broker-ca <path>` | PEM bundle the broker's upstream TLS client trusts (default: system roots) |
| `--broker-endpoint <spec>` | One broker endpoint as `;`-separated `key=value` pairs. Repeatable; persisted |
| `--mediation p=host:port` | Guest-to-host [mediation channel](/concepts/glossary/) — a vsock (VM socket) path into your host control plane |
| `--mediation-optional` | Allow startup when mediation is unavailable |

The default `broker` mode forwards traffic opaquely — no certificate is forged
and no CA is installed in the guest; TLS interception exists only in `mitm`,
which is why `--egress-swap-config` and `--cred-swap` require it. Broker
credentials and cred-swap refs are always references (`env:NAME` / `file:PATH`
/ `vault:PATH`), never literal secrets, and the guest never holds the real
key. For the full semantics — modes, allow vs passthrough, credential swap,
the broker decision stream — see
[egress mediation](/concepts/egress-mediation/) and the
[allowlist how-to](/guides/egress-allowlist/).

A `--broker-endpoint` spec bundles `upstream=<url>;secret=NAME=<scheme>:<ref>;base-url-env=KEY[=VALUE];ca=<path>;proxy;capture`
into one flag; repeat it for multiple endpoints (all persist across
restart/wake), and don't combine it with the individual `--broker-*` flags.

### Model runner & mediation

| Flag | Description |
|---|---|
| `--model <ref>` | Pair the workspace with a locally served HuggingFace GGUF model. See [`model`](/cli/model/) |
| `--model-token <token>` | HuggingFace token for auto-pull; defaults to `HF_TOKEN` / `HUGGING_FACE_HUB_TOKEN` |
| `--model-runner <backend>` | Runner backend: `llamacpp`, `vllm`, or `custom` |
| `--model-gpu <mode>` | GPU intent: `off`, `on`, or `auto` |
| `--model-runner-model <id>` | Backend model id for runners such as vLLM |
| `--model-runner-served-model <name>` | OpenAI-compatible served model name |
| `--model-runner-command <template>` | Custom OpenAI-compatible host runner command template |
| `--model-runner-name <name>` | Custom host model runner name override |
| `--model-runner-health-path <path>` | Custom host model runner health probe path |
| `--model-runner-arg <arg>` | Extra runner argument. Repeatable |
| `--model-runner-env KEY=VALUE` | Extra runner env for this invocation. Repeatable; not persisted |
| `--model-mediation <mode>` | Model mediation mode: `off`, `local-allow`, or `policy` |
| `--model-policy-file <path>` | Policy file for `--model-mediation policy` |
| `--model-policy-url <url>` | External policy endpoint for `--model-mediation policy` |
| `--model-policy-timeout <duration>` | Policy timeout, such as `250ms` or `2s` |

The model pairing and runner settings persist with the workspace (except
`--model-runner-env`), so every `start` re-pairs the model and injects
`MICROAGENT_MODEL_URL` / `OPENAI_BASE_URL` into the guest.

### Low-level & output

| Flag | Description |
|---|---|
| `--backend <name>` | Backend identity override |
| `--kernel <path>` | Custom kernel path |
| `--rootfs <path>` | Use an existing ext4 rootfs; enables `--id` and `--role` (see [Examples](#examples)) |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |
| `--guest-init <path>` | Guest init path |
| `--arch <arch>` | Guest architecture (`arm64`/`aarch64`, `amd64`/`x86_64`) |
| `--result-port <port>` | Vsock result port |
| `--mke2fs <path>` | mke2fs binary path |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--request-json <path\|->` | Read request JSON from a file or stdin (`-json` after the subcommand is a compat alias) |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`/`--supervisor`.

## Fork from a snapshot

`create <name> --from-snapshot <workspace>:<tag>` forks a new workspace from an
existing workspace's [snapshot](/cli/snapshot/) instead of building from an
image. The fork gets a fresh identity and a private copy of the snapshot's
rootfs, then resumes from the snapshot's memory and device state.

The snapshot kernel must match. In-flight guest connections do not survive the
fork - the guest process must reconnect.

Networked forks use `user` mode. Each fork gets its own runtime network path,
so multiple forks can run concurrently without colliding.

## Image references

`--image` accepts both digest-pinned references (`docker.io/library/ubuntu@sha256:…`) and mutable tags (`docker.io/library/ubuntu:24.04`). Both are allowed here - `create` records the resolved digest in the workspace verification record so `microagent --json status` can flag drift later. Pin by digest if you want reproducible workspaces.

[`microagent rootfs build`](/cli/rootfs/) is stricter: it rejects mutable tags unless you pass `--allow-mutable`. See [security](/security/) for the rationale.

## Exit status

`create` exits `0` on success (including a successful `--dry-run` validation);
nonzero when validation fails, the image cannot be fetched, or the rootfs build
fails. In AX mode a failure is written as a structured error envelope.

## Related

- [`start`](/cli/start/) - boot the workspace you created
- [`stop`](/cli/stop/) - shut it down again
- [`delete`](/cli/delete/) - remove it and its state
- [State and identity](/concepts/state-and-identity/) - what the workspace record holds
- [Network modes](/concepts/networking/) - `user`, `isolated`, and published ports
