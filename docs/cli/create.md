---
title: microagent create
description: Create a named workspace that survives between starts.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-09_

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
microagent create --json request.json
```

For JSON output from the create command, put the global flag before the
subcommand:

```bash
microagent --json create research --image docker.io/library/ubuntu:24.04
```

## Flags

Flags you'll actually use:

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

The complete set:

| Flag | Description |
|---|---|
| `--image <ref>` | OCI image reference. When omitted on the image path, defaults to Python 3.13 slim (digest-pinned for `arm64`/`amd64`, the `python:3.13-slim` tag for other architectures); the `--rootfs` and `--from-snapshot` paths take no image |
| `--from-snapshot <workspace>:<tag>` | Fork a new workspace from an existing workspace's snapshot instead of an image |
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
| `--secret NAME=<scheme>:<ref>` | Deliver a secret to `/run/secrets/NAME` over vsock, re-resolved each start. Repeatable. See [`secret`](/cli/secret/) |
| `--secrets-env-file <path>` | Deliver every key in a dotenv file as a secret (plaintext, re-read each start) |
| `--secret-on-demand NAME=<scheme>:<ref>` | Declare an on-demand secret fetched at runtime via `$MICROAGENT_SECRETS_SOCK`, never written to tmpfs. Repeatable. See [`secret`](/cli/secret/) |
| `--secrets-audit` | Append every secret access to the workspace audit log (`microagent secret audit`) |
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
| `--restart <policy>` | Restart policy: `never`, `on-failure`, or `always`. Enforced by [`supervise`](/cli/supervise/) |
| `--network <mode>` | Network mode: `user` (default) or `isolated` |
| `--publish <mapping>` | Declarative TCP host port forward, `[host:]hostPort:guestPort[/tcp]`. Repeatable |
| `-p <mapping>` | Alias for `--publish` |
| `--mediation p=host:port` | Declare the guest-to-host mediation vsock channel |
| `--mediation-optional` | Allow startup when mediation is unavailable |
| `--egress <mode>` | [Egress mediation](/concepts/egress-mediation/) mode: `guarded` (default; deny the inside, allow public), `broker` (allow-broad, opaque forward-proxy — no certificate forged, no CA in the guest), `strict` (deny non-allowlisted), or `off`. Persisted with the workspace |
| `--egress-lock-allowlist` | In `--egress broker`, restrict egress to allowlisted destinations only (drop the allow-broad default), keeping the opaque-splice, no-CA behavior |
| `--egress-allow <host>` | Allowlisted egress destination (TLS-intercepted). Repeatable; an exact host or a `.suffix` matching the apex and subdomains. See the [allowlist how-to](/guides/egress-allowlist/) |
| `--egress-passthrough <host>` | Allowed egress destination that is **not** TLS-intercepted (forwarded opaquely). Repeatable. For cert-pinned / mTLS endpoints |
| `--egress-policy <path>` | Egress policy file (`.yaml`/`.yml`/`.json`) declaring `allow[]` / `passthrough[]`; unioned with the flags. Requires `--egress guarded` or `strict` |
| `--egress-swap-config <path>` | Credential-swap config (YAML): for an allowlisted, intercepted host the mediator injects the real credential host-side so the guest never holds it. Requires `--egress guarded` or `strict`. See [credential swap](/concepts/egress-mediation/#credential-swap) |
| `--cred-swap PROVIDER[=ref]` | Shorthand for a credential swap against a built-in provider (`anthropic`, `openai`, `gemini`, `groq`, `openrouter`, `deepseek`): allowlists the provider host and injects its API key host-side so the guest never holds it. The optional `=ref` is a reference (`env:NAME` / `file:PATH` / `vault:PATH`), never a literal secret; omitted, it defaults to the provider's conventional env var. Repeatable; requires `--egress guarded` or `strict`. See [credential swap](/concepts/egress-mediation/#credential-swap) |
| `--broker-upstream <url>` | Route egress through the [egress broker](/concepts/egress-mediation/): a host-side proxy that injects the credential and originates its own upstream TLS, so the guest never holds the key. Persisted with the workspace |
| `--broker-secret NAME=<scheme>:<ref>` | Broker credential; the guest sends `@secret:NAME` references and the broker swaps in the live value host-side. A reference (`env:NAME` / `file:PATH` / `vault:PATH`), never a literal secret. Required with `--broker-upstream` |
| `--broker-env KEY[=VALUE]` | Guest env var pointed at the broker; an empty `VALUE` is filled with the broker URL (e.g. `--broker-env ANTHROPIC_BASE_URL`). Repeatable |
| `--broker-proxy` | Also set `HTTPS_PROXY` / `HTTP_PROXY` in the guest to the broker (CONNECT tunneling) |
| `--broker-capture` | Opt in to raw capture of pre-swap broker requests (path, headers with references, bounded body) to an owner-only per-workspace file. Off by default — the default record is the minimized decision stream. See [broker observability](/concepts/egress-mediation/#the-broker-decision-stream) |
| `--model <ref>` | Pair the workspace with a locally served HuggingFace GGUF model; the ref is persisted so every `start` re-pairs, and `MICROAGENT_MODEL_URL` / `OPENAI_BASE_URL` are injected into the guest. See [`model`](/cli/model/) |
| `--model-token <token>` | HuggingFace token for model auto-pull; defaults to `HF_TOKEN` or `HUGGING_FACE_HUB_TOKEN` when omitted |
| `--model-runner <backend>` | Model runner backend: `llamacpp`, `vllm`, or `custom`; persisted with the workspace |
| `--model-gpu <mode>` | Model runner GPU intent: `off`, `on`, or `auto`; persisted with the workspace |
| `--model-runner-model <id>` | Backend model id for runners such as vLLM; persisted with the workspace |
| `--model-runner-served-model <name>` | OpenAI-compatible served model name for runners such as vLLM |
| `--model-runner-command <template>` | Custom OpenAI-compatible host runner command template; persisted with the workspace |
| `--model-runner-arg <arg>` | Extra model runner argument. Repeatable; persisted with the workspace |
| `--model-runner-env KEY=VALUE` | Extra model runner environment for this invocation. Repeatable; values are not persisted |
| `--model-mediation <mode>` | Model mediation mode: `off`, `local-allow`, or `policy`; persisted with the workspace |
| `--model-policy-file <path>` | Structured model mediation policy file for `--model-mediation policy` |
| `--model-policy-url <url>` | External model mediation policy endpoint for `--model-mediation policy` |
| `--model-policy-timeout <duration>` | Model mediation policy timeout, such as `250ms` or `2s` |
| `--memory <MiB>` | Memory in MiB (default 512) |
| `--cpus <n>` | CPU count |
| `--size-mib <MiB>` | Rootfs disk size |
| `--result-port <port>` | Vsock result port |
| `--mke2fs <path>` | mke2fs binary path |
| `--supervisor <path>` | Override the installed host backend supervisor path |
| `--dry-run` | Validate config without creating |
| `--json <path\|->` | Read request JSON from a file or stdin; separate from the global output flag |

See [global flags](/cli/#global-flags) for `--text`/`--output`/`--mode`/`--supervisor` and the global `--json` output flag (distinct from the `--json` request-input flag above).

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
