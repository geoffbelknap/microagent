---
title: Workspace spec
description: Declarative microagent.yaml format for reproducible creates.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-12_

`microagent.yaml` records the inputs needed to recreate a workspace from source
control. It is the declarative form of [`microagent create`](/cli/create/):
each field corresponds to a `create` flag, and when both are given, CLI flags
override matching spec fields. Use the file when the workspace definition
should live in the repo; use flags for one-off overrides.

Pass it explicitly with `--file` to `create`, [`run`](/cli/run/), or
[`dispatch`](/cli/dispatch/). A spec in the current directory is never read
implicitly. With the optional `agent:` block (below), a spec doubles as an
**Agentfile** — a build-free recipe for running an agent in an isolated
workspace.

A minimal spec needs only a few lines:

```yaml
name: research
image: docker.io/library/ubuntu:24.04
profile: medium
setup:
  - mkdir -p /workspace
entrypoint: /app/start.sh
```

Everything else is optional. The kitchen-sink example:

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
modelRunner:
  backend: llamacpp
  gpu: off
  args: ["--no-ui"]
modelMediation:
  mode: policy
  policyFile: ./model-policy.json
  policyTimeout: 250ms
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
agent:
  entry: python /app/agent.py
  egress: mitm
  allow: [api.anthropic.com]
  lockAllowlist: true
  cred-swap: [anthropic]
  broker:
    upstream: https://api.anthropic.com
    secret: anthropic=env:ANTHROPIC_API_KEY
    env: [ANTHROPIC_BASE_URL]
```

## Agentfile: the `agent:` block

The optional `agent:` block turns a spec into an **Agentfile**. It carries the
few agent-defining knobs the rest of the spec cannot express, while base image,
dependency install, files, and env reuse the normal top-level fields. There is no
image to build. `microagent dispatch --file agent.yaml` pulls the thin base, runs
`setup` in the booted guest, drops `files`, and runs `entry` under the egress
envelope — installing the SDK at boot rather than baking a fat image. See
[examples/agents](https://github.com/geoffbelknap/microagent/tree/main/examples/agents).

```bash
microagent dispatch --file agent.yaml
```

CLI flags override the block (for example `--egress mitm` beats `agent.egress`,
`--exec` beats `agent.entry`); `agent.allow` and `agent.cred-swap` union with the
corresponding flags. A `--broker-*`/`--broker-endpoint` broker supplied on the
command line wins outright — `agent.broker`/`agent.brokers` only fills an
otherwise-unset broker.

## Usage

```bash
microagent create --file microagent.yaml
```

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
| `service` | Long-running shell command to run as the VM service (the `--service-command` flag) |
| `shell` | Interactive console shell path. Defaults to `/bin/sh`; the path must exist inside the guest |
| `hostname` | Guest hostname. Defaults to the workspace name sanitized as a Linux hostname |
| `setup` | Commands to run before first start |
| `setupFiles` | Host script files whose contents run as setup commands after `setup`; paths relative to the spec file or absolute |
| `files` | Source files to copy into the workspace rootfs |
| `files[].src` | Host path, relative to the spec file or absolute |
| `files[].dst` | Absolute guest path to write |
| `files[].mode` | Optional octal file mode string, such as `"0755"` |
| `env` | Guest environment variables |
| `model` | HuggingFace GGUF ref of a locally served model to pair the workspace with; every `start` re-pairs it, and a CLI `--model` flag overrides the field. See [`model`](/cli/model/) |
| `modelRunner.backend` | Model runner backend: `llamacpp`, `vllm`, or `custom` |
| `modelRunner.gpu` | Model runner GPU intent: `off`, `on`, or `auto` |
| `modelRunner.backendModel` | Backend model id for runners such as vLLM |
| `modelRunner.servedModel` | OpenAI-compatible served model name for runners such as vLLM |
| `modelRunner.command` | Custom runner argv template; supports `{model}`, `{host}`, `{port}`, and `{addr}` |
| `modelRunner.name` | Custom runner name recorded in runner state |
| `modelRunner.healthPath` | Custom runner health probe path |
| `modelRunner.args` | Extra runner argv entries |
| `modelMediation.mode` | Model mediation mode: `off`, `local-allow`, or `policy` |
| `modelMediation.policyFile` | Structured model mediation policy file path |
| `modelMediation.policyURL` | External model mediation policy endpoint URL |
| `modelMediation.policyTimeout` | Model mediation policy timeout, such as `250ms` or `2s` |
| `resources.memoryMiB` | Memory override |
| `resources.cpuCount` | CPU override |
| `resources.sizeMiB` | Rootfs disk size override |
| `resources.headroomMiB` | Writable space guaranteed beyond the image content when the size is derived (default: 512) |
| `network.mode` | Network mode: `user` (default) or `isolated` |
| `network.forwards` | Published ports; each entry takes `protocol` (default `tcp`), `host`, `hostPort`, `guestPort` |
| `network.dns` | Guest DNS server list |
| `network.routes` | Extra guest static routes |
| `network.ip` | Static guest IP override |
| `network.subnet` | Guest subnet override |
| `network.gateway` | Guest gateway override |
| `network.ipv6` | Static guest IPv6 CIDR override |
| `network.ipv6Subnet` | Guest IPv6 subnet override |
| `network.ipv6Gateway` | Guest IPv6 gateway override |
| `mediation` | Guest-to-host vsock mediation channel contract |
| `mediation.enabled` | Enables the mediation declaration |
| `mediation.required` | Requires the channel for workspace startup |
| `mediation.port` | Guest vsock port used by the agent |
| `mediation.target` | Host address and port for the enforcer/orchestrator |
| `mediation.failClosed` | Treats a required channel break as closed by default |
| `health` | Liveness probe; an unhealthy workspace is restarted by [`supervise`](/cli/supervise/) under the restart policy |
| `health.exec` | Probe command run in the guest through structured exec when the selected backend exposes `execReady`; healthy on exit 0. Declare either `exec` or `httpGet` |
| `health.httpGet` | Probe path for a host-side GET against a published guest port (for example `/healthz`); healthy on a non-error status |
| `health.port` | Published guest port the `httpGet` probe targets |
| `health.intervalSeconds` | Seconds between probes (default 30) |
| `health.timeoutSeconds` | Per-probe timeout (default 5) |
| `health.retries` | Consecutive failures before the workspace is considered unhealthy (default 3) |
| `health.startPeriodSeconds` | Grace period after start before probing begins (default 0) |
| `disks` | Existing ext4 disks to attach |
| `disks[].sourcePath` | Path the disk content came from; for `bundles:` entries it is the source tar (defaults to `path`) |
| `disks[].bundle` | Set automatically: `false` for `disks:` entries, `true` for `bundles:` entries (visible in persisted manifests) |
| `bundles` | Tar bundles to build into ext4 disks and attach |
| `outputs` | Declared output artifact paths inside the workspace |
| `agent.entry` | The agent's run command (the one-shot exec); a CLI `--exec` overrides it |
| `agent.egress` | Egress mode: `broker`, `mitm`, or `off`; a CLI `--egress` overrides it |
| `acknowledgeCapabilityRisk` | Top-level operator reason accepting private data plus injected files/disks plus unmediated outbound; persisted with the workspace |
| `agent.allow` | Extra egress hosts to allowlist; unioned with `--egress-allow` |
| `agent.lockAllowlist` | Drop the allow-broad grant. On `apply`, `true` replaces the prior allowlist with `agent.allow` and clears old passthrough hosts; a running workspace must halt/start |
| `agent.cred-swap` | Built-in providers to inject host-side, each `PROVIDER[=env:NAME\|file:PATH\|vault:PATH]` (reference only, never a literal); unioned with `--cred-swap`. See [credential swap](/concepts/egress-mediation/#credential-swap) |
| `agent.broker.upstream` | Egress broker upstream base URL; the broker injects the credential host-side and originates its own TLS, so the guest never holds the key. A CLI `--broker-upstream` overrides the block |
| `agent.broker.secret` | Broker credential `NAME=<scheme>:<ref>` (reference only, never a literal); held host-side only, the guest sends `@secret:NAME` references |
| `agent.broker.env` | Guest env vars pointed at the broker, each `KEY[=VALUE]` (empty value = the broker URL) |
| `agent.broker.proxy` | Also set `HTTPS_PROXY`/`HTTP_PROXY` in the guest to the broker (CONNECT tunneling) |
| `agent.broker.capture` | Opt in to raw capture of pre-swap broker requests to an owner-only file; off by default (the default record is the minimized decision stream) |
| `agent.broker.ca` | PEM bundle path this broker's upstream TLS client trusts; empty means system roots |
| `agent.brokers` | Declare multiple broker endpoints instead of a single `agent.broker`; a list of blocks with the same `upstream`/`secret`/`env`/`proxy`/`capture`/`ca` fields, one per endpoint. Setting both `agent.broker` and `agent.brokers` is rejected |

The less obvious fields in YAML form - a long-running service, setup from a
script file, and the `network:` block:

```yaml
service: /usr/local/bin/homebridge
setupFiles:
  - ./setup.sh          # file contents run as one setup command
network:
  mode: user
  forwards:
    - protocol: tcp
      host: 127.0.0.1
      hostPort: 8581
      guestPort: 8581
  dns: [1.1.1.1]
```

## Related

- [`create`](/cli/create/) - the command this file drives
- [`apply`](/cli/apply/) - apply spec changes to an existing workspace
- [`profiles`](/cli/profiles/) - the named resource profiles
- [`supervise`](/cli/supervise/) - acts on `restart` and `health`
