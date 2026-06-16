---
title: microagent model
description: Download and manage local HuggingFace GGUF model files.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-16_

```text
microagent model pull <hf-ref> [--token <t>] [--state-dir <dir>]                  Download a GGUF model
microagent model list [--state-dir <dir>]                                           List stored models
microagent model delete <ref> [--keep-files] [--state-dir <dir>]                      Remove a model and its blob
microagent model prune [--delete-files] [--state-dir <dir>]                       Drop records for missing blobs
microagent model serve <hf-ref> [--dedicated] [--runner-command <template>] [--runner-name <name>] [--runner-health-path <path>] [--runner-arg <arg>] [--runner-env KEY=VALUE] [--token <t>] [--state-dir <dir>]   Serve a model on the host
microagent model serve <hf-ref> [--dedicated] [--runner-command <template>] [--runner-name <name>] [--runner-health-path <path>] [--runner-arg <arg>] [--runner-env KEY=VALUE] [--token <t>] [--state-dir <dir>]   Alias for model serve
microagent model stop <hf-ref> [--state-dir <dir>]                                Stop a model's runners
microagent model runners [--state-dir <dir>]                                      List running model servers
microagent model policy validate <policy.json>                                    Validate a mediation policy file
microagent model policy evaluate <policy.json> [options]                          Dry-run a policy file against structured request metadata
```

`model` manages a local content-addressed store of GGUF model files and the
host model server processes that serve them. Downloaded blobs are stored under
`~/.microagent/models/` by default, indexed by the HuggingFace reference used
to pull them. All subcommands read and write this index; no remote state is
modified by the store commands. The server commands (`serve`, `stop`,
`runners`) manage long-running host model runner processes. The built-in
default runner is `llama-server`, but the runner command is configurable. Pair
a workspace with a served model using [`run --model`](/cli/run/) for one-shots
or [`create --model`](/cli/create/) for a persistent pairing that every
[`start`](/cli/start/) re-establishes.

## Examples

Download and manage stored models:

```bash
# Download a public model
microagent model pull TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf

# Download a gated model with an explicit token
microagent model pull hf.co/meta-llama/Llama-2-7B/llama-2-7b.gguf --token hf_xxxxx

# List stored models
microagent model list

# Remove a model and delete its blob
microagent model delete TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf

# Remove records for missing blobs (safe; no files deleted)
microagent model prune
```

Serve a model and manage the runners:

```bash
# Start a shared pinned model server (auto-pulls if not stored)
microagent model serve TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf

# Start a dedicated runner (exclusive to this caller)
microagent model serve TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf --dedicated

# Pass host runner arguments to the selected runner
microagent model serve TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf \
  --runner-arg -ngl --runner-arg all

# Use a custom OpenAI-compatible host runner command template
microagent model serve TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf \
  --runner-command 'runner serve {model} --host {host} --port {port}' \
  --runner-name runner

# List running model servers
microagent model runners

# Stop all runners for a model
microagent model stop TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf
```

`model list` prints one tab-separated row per recorded model (no header):

```text
hf.co/TheBloke/Llama-2-7B-GGUF@main/llama-2-7b.Q4_K_M.gguf	3825819648	sha256:abc...
```

With the global `--json` flag, records are returned under `models`:

```json
{
  "models": [
    {
      "model_ref": "hf.co/TheBloke/Llama-2-7B-GGUF@main/llama-2-7b.Q4_K_M.gguf",
      "resolved_ref": "https://huggingface.co/TheBloke/Llama-2-7B-GGUF/resolve/main/llama-2-7b.Q4_K_M.gguf",
      "digest": "sha256:abc...",
      "size_bytes": 3825819648,
      "output_path": "/home/user/.microagent/models/blobs/a1b2c3d4e5f6a7b8c9d0e1f2.gguf",
      "last_used_at": "2026-06-01T12:00:00Z"
    }
  ]
}
```

## Commands

| Command | Description |
|---|---|
| `pull` | Download a GGUF file from HuggingFace and record it in the local store |
| `list` | List locally stored model records (ref, size, digest) |
| `delete` | Remove a model record, and optionally delete its blob |
| `prune` | Remove records whose blob files are missing; with `--delete-files`, also delete all indexed blob files |
| `serve` | Start (or reuse) a pinned host model server for a model; auto-pulls if not yet stored |
| `stop` | Force-stop all model server processes for a model ref |
| `runners` | List currently running model server processes |
| `policy validate` | Validate a structured model mediation policy file |
| `policy evaluate` | Dry-run a policy file against structured request metadata |

Without a custom command, `serve` uses the built-in `llama-server` runner
resolver. It requires `llama-server` on PATH or set via the
`MICROAGENT_LLAMA_SERVER` environment variable. If neither is present, the
command exits with a clear error message. If the model is not yet in the local
store, `serve` pulls it automatically before starting the server (equivalent to
running `model pull` first). The runner is started pinned, so it stays alive
even when no workspace holds it.

For Linux x86_64 hosts with NVIDIA CUDA, the dev build helper can reproduce the
CUDA `llama-server` build used by microagent model-runner testing:

```bash
scripts/dev/build-llama-cuda.sh --llama-dir ../llama.cpp

export MICROAGENT_LLAMA_SERVER=/tmp/llama.cpp-cuda13-ninja-build/bin/llama-server
export MICROAGENT_MODEL_RUNNER_ARGS='["-ngl","all","--no-ui"]'
```

microagent starts the default llama.cpp runner with `--device none
--gpu-layers 0` unless you explicitly pass GPU-related runner args. Pointing
`MICROAGENT_LLAMA_SERVER` at a CUDA-enabled binary is therefore not enough to opt
into GPU use; pass args such as `["-ngl","all"]` or `--runner-arg -ngl
--runner-arg all`.

By default the helper downloads pinned CUDA 13.3 Ubuntu 24.04 debs, verifies
their SHA256 checksums, extracts them under `/tmp/microagent-cuda13-root`
without installing system packages, builds llama.cpp out of tree with Ninja,
and verifies `llama-server --list-devices`. Override `--cuda-arch` for GPUs
other than the RTX 3080 Ti's compute capability `86`, or pass `--cuda-home`
to use an existing CUDA toolkit. The script does not install dependencies; if
`cmake`, `ninja`, `curl`, `dpkg-deb`, `sha256sum`, `g++`, or `git` is missing,
install it explicitly and rerun the helper.

Set `MICROAGENT_MODEL_RUNNER_COMMAND` or pass `--runner-command` to use another
OpenAI-compatible host runner. The command is parsed as argv fields, not shell
evaluated. It must include `{model}` and either `{port}` or `{addr}`; `{host}`
is also available. The environment variable accepts shell-like fields or a JSON
string array:

```bash
MICROAGENT_MODEL_RUNNER_COMMAND='runner serve {model} --host {host} --port {port}' \
  MICROAGENT_MODEL_RUNNER_NAME=runner \
  microagent start research

MICROAGENT_MODEL_RUNNER_COMMAND='["runner","serve","{model}","--listen","{addr}"]' \
  microagent model serve TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf
```

Use `MICROAGENT_MODEL_RUNNER_NAME` and
`MICROAGENT_MODEL_RUNNER_HEALTH_PATH` to label a custom runner and change its
readiness probe path; the matching one-shot flags are `--runner-name` and
`--runner-health-path`.

Runner arguments are opaque host-runner configuration. Use repeatable
`--runner-arg` flags for a single `model serve` invocation, or set
`MICROAGENT_MODEL_RUNNER_ARGS` to apply defaults to any model runner that
microagent starts, including workspace `run --model`, `create --model`, and
later `start` re-pairing. The environment variable accepts shell-like fields
or a JSON string array:

```bash
MICROAGENT_MODEL_RUNNER_ARGS='-ngl all' microagent model serve \
  TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf

MICROAGENT_MODEL_RUNNER_ARGS='["-ngl","all"]' microagent start research
```

Use repeatable `--runner-env KEY=VALUE` flags for a single `model serve`
invocation, or `MICROAGENT_MODEL_RUNNER_ENV` for defaults. The environment
variable accepts shell-like `KEY=VALUE` fields, a JSON string array, or a JSON
object. Runner env keys, not values, are recorded in the runner registry.

Workspaces hold runners. `run --model` holds one for the duration of the run.
A workspace created with `create --model` re-pairs on every `start` and holds
until `halt`, `stop`, `kill`, or `delete` releases it - a guest that exits on
its own keeps its hold until the next lifecycle verb. An unpinned runner stops
when its last holder releases; a pinned one (`model serve`) stays up.
When an existing workspace attaches or releases a model runner, microagent
appends `model_worker=attached` and `model_worker=released` markers to the
workspace's [`events`](/cli/events/) history. These markers record the model
ref, holder, runner engine, process ID, and runner config digest for tracing.
Runner environment values are not recorded.

`stop` force-stops all model server processes for the given ref (ignores
whether the runner is pinned) and removes their entries from the runner index.
Use it to reclaim a runner whose workspace exited without a lifecycle verb.

`runners` self-heals the registry: any listed process that is no longer alive
is silently removed before the list is printed.

## Mediated backend validation

The production model mediation path is still experimental, but the developer
E2E suite can validate it against the supported external backends without
making GPU access a default requirement:

```bash
# Stub OpenAI-compatible runner; no GPU or llama.cpp required.
MICROAGENT_E2E_MODEL_MEDIATION=1 scripts/dev/microagent-e2e.sh model-mediation

# Runner-neutral matrix for any prepared OpenAI-compatible runner.
MICROAGENT_E2E_MODEL_MEDIATION_RUNNER=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_MODEL_REF=org/repo/model.gguf \
  MICROAGENT_MODEL_RUNNER_COMMAND='runner serve {model} --host {host} --port {port}' \
  MICROAGENT_MODEL_RUNNER_NAME=runner \
  scripts/dev/microagent-e2e.sh model-mediation-runner

# Policy file generation/validation smoke; no VM or model runner.
MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_POLICY_ONLY=1 \
  scripts/dev/microagent-e2e-model-mediation-runner.sh

# Runner-neutral matrix through a fake custom runner; no GPU or real model.
MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE=1 \
  scripts/dev/microagent-e2e.sh model-mediation-runner-fake

# Runner-neutral pressure probe through the fake custom runner.
MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_PRESSURE=1 \
  scripts/dev/microagent-e2e.sh model-mediation-runner-fake

# CI-safe fake pressure preset with required gates.
MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_PRESSURE=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_PRESSURE_PRESET=ci \
  scripts/dev/microagent-e2e.sh model-mediation-runner-fake

# llama.cpp runner, default CPU execution.
MICROAGENT_E2E_MODEL_MEDIATION_LLAMA=1 \
  MICROAGENT_LLAMA_SERVER=/path/to/llama-server \
  scripts/dev/microagent-e2e.sh model-mediation-llamacpp

# llama.cpp CPU pressure probe.
MICROAGENT_E2E_MODEL_MEDIATION_LLAMA=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_PRESSURE=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_GPU=0 \
  MICROAGENT_LLAMA_SERVER=/path/to/llama-server \
  scripts/dev/microagent-e2e.sh model-mediation-llamacpp

# Bounded llama.cpp hardware pressure preset.
MICROAGENT_E2E_MODEL_MEDIATION_LLAMA=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_PRESSURE=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_PRESSURE_PRESET=hardware \
  MICROAGENT_LLAMA_SERVER=/path/to/llama-server \
  scripts/dev/microagent-e2e.sh model-mediation-llamacpp

# llama.cpp runner with explicit GPU offload.
MICROAGENT_E2E_MODEL_MEDIATION_LLAMA=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_GPU=1 \
  MICROAGENT_LLAMA_SERVER=/path/to/llama-server \
  scripts/dev/microagent-e2e.sh model-mediation-llamacpp

# vLLM GPU runner from a local checkout.
MICROAGENT_E2E_MODEL_MEDIATION_VLLM=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_VLLM_REPO=../vllm \
  scripts/dev/microagent-e2e.sh model-mediation-vllm

# vLLM GPU pressure probe.
MICROAGENT_E2E_MODEL_MEDIATION_VLLM=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_VLLM_PRESSURE=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_VLLM_REPO=../vllm \
  scripts/dev/microagent-e2e.sh model-mediation-vllm

# Bounded vLLM hardware pressure preset.
MICROAGENT_E2E_MODEL_MEDIATION_VLLM=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_VLLM_PRESSURE=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_VLLM_PRESSURE_PRESET=hardware \
  MICROAGENT_E2E_MODEL_MEDIATION_VLLM_REPO=../vllm \
  scripts/dev/microagent-e2e.sh model-mediation-vllm
```

The runner-neutral matrix runs direct, local-allow, external policy allow,
external policy deny, file-policy allow, file-policy deny, and policy
unavailable cases. It also validates and dry-runs generated file policies with
`microagent model policy validate` and `microagent model policy evaluate`
before booting the corresponding guest probes. Set
`MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_POLICY_ONLY=1` to run only those
generated-policy checks without KVM, Firecracker, a guest image, or a model
runner. Set `MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE=1` to run the same
matrix through a tiny fake OpenAI-compatible custom runner; that path still
boots microVM probes, but it does not need llama.cpp, vLLM, a GPU, a real model,
or HuggingFace access. For the live runner-neutral matrix, provide a model ref
plus the same custom runner environment accepted by `model serve`, such as
`MICROAGENT_MODEL_RUNNER_COMMAND`, `MICROAGENT_MODEL_RUNNER_NAME`,
`MICROAGENT_MODEL_RUNNER_HEALTH_PATH`, `MICROAGENT_MODEL_RUNNER_ARGS`, and
`MICROAGENT_MODEL_RUNNER_ENV`.

The fake, llama.cpp, and vLLM scenarios are adapters over that same matrix:
they handle runner-specific preflight and startup, then delegate the mediated
request cases to the shared harness. The matrices emit profile summaries,
direct-vs-mediated comparisons, telemetry summaries, and mediation gate TSVs
under their output directories. Gates fail the scenario by default when local
mediation, policy mediation, or decision latency exceeds the configured budget;
set the scenario-specific `*_GATE_MODE=warn` only when collecting noisy
experimental data. The model mediation scenarios default to
`quay.io/curl/curl:latest` for the guest HTTP probe image; use the
scenario-specific `*_IMAGE` override when a different internal mirror is
required.

Set the adapter-specific `*_PRESSURE=1` switch to replace the functional
allow/deny matrix with the runner-neutral pressure probe. The pressure probe
compares direct bridge traffic with local mediation, file-policy allow, and
external-policy allow across configurable guest workspaces and per-workspace
concurrency. It writes `pressure-profiles.tsv`,
`pressure-profile-comparison.tsv`, `pressure-audit-summary.tsv`, optional
telemetry summaries, `pressure-gates.tsv`, and compact
`pressure-decision.txt` / `pressure-decision.tsv` / `pressure-decision.json`
reads. Start with `pressure-decision.txt` for the run status, worst positive
direct-vs-mediated deltas, policy decision p95, and telemetry read; use the raw
TSVs when you need endpoint- or audit-level detail. Pressure gates default to
`warn`, because this path is intended to establish realistic concurrency
budgets before making them release-blocking. Common knobs are
`*_PRESSURE_WORKSPACES`, `*_PRESSURE_CONCURRENCY`, `*_PRESSURE_CASES`,
`*_PRESSURE_WARMUPS`, and `*_PRESSURE_GATE_MODE`.

The adapters also accept `*_PRESSURE_PRESET=ci|hardware|baseline|default`.
`ci` is intended for the fake runner: one workspace, concurrency `1`, one
sample, no warmup, telemetry off, short token caps, and required gates
(`models <= 100ms`, `chat <= 250ms`, `stream TTFB <= 100ms`,
`decision <= 50ms`). `hardware` is intended for llama.cpp and vLLM collection:
one workspace, concurrency `1,2`, one sample, no warmup, short token caps,
telemetry auto, and warn gates (`models <= 100ms`, `chat <= 500ms`,
`stream TTFB <= 250ms`, `decision <= 100ms`). `baseline` and `default` keep
the previous adapter defaults. Explicit `*_PRESSURE_*` env vars always override
preset values.

Set `MICROAGENT_MODEL_MEDIATION=policy` to require a policy source for the
host-worker mediator. The source can be either an external decision endpoint
with `MICROAGENT_MODEL_POLICY_URL=http://127.0.0.1:9000/decision`, or a local
structured policy file with `MICROAGENT_MODEL_POLICY_FILE=/path/to/policy.json`.
The two sources are mutually exclusive. File policies fail closed by default
and inspect only structured request metadata and aggregate body counts; prompt
text is not written into mediation audit logs.

```json
{
  "schema_version": "microagent.model_policy.v1",
  "default": "deny",
  "rules": [
    {
      "id": "small-chat",
      "effect": "allow",
      "match": {
        "methods": ["POST"],
        "paths": ["/v1/chat/completions"],
        "models": ["local-model"]
      },
      "limits": {
        "max_request_bytes": 32768,
        "max_text_bytes": 4096,
        "max_messages": 16,
        "max_tokens": 512,
        "stream": false,
        "allowed_tool_names": ["shell", "read_file"]
      }
    }
  ]
}
```

The file policy match fields are `workspace_ids`, `capabilities`, `worker_ids`,
`methods`, `paths`, and `models`; empty match fields are wildcards. `paths`
matches the request path received by the mediator. For the default
`MICROAGENT_MODEL_URL` exposed inside a workspace, that path includes `/v1`.
Limit fields are `max_request_bytes`, `max_text_bytes`, `max_messages`,
`max_tokens`, `stream`, and `allowed_tool_names`. If an allow rule matches but
a limit fails, the request is denied rather than falling through to a later
rule.

File policy can mediate the request method, path, workspace/capability/worker
identity, declared model, declared stream mode, declared token cap, declared
tool/function names, request bytes, message count, and aggregate text bytes. It
does not inspect prompt meaning, response content, semantic tool intent, quotas,
trust scores, billing rules, or user/business authorization. Use the external
policy URL path when those decisions need a policy service; microagent still
owns the fail-closed substrate enforcement around that decision.

Validate a generated file before using it:

```bash
microagent model policy validate ./model-policy.json
microagent --json model policy validate ./model-policy.json
```

Dry-run a structured request without starting a model runner or VM:

```bash
microagent model policy evaluate ./model-policy.json \
  --method POST \
  --path /v1/chat/completions \
  --model local-model \
  --max-tokens 128 \
  --stream false \
  --tool shell \
  --text-bytes 512 \
  --messages 1 \
  --expect allow
```

`policy evaluate` exits nonzero only when the policy file is invalid, the
sample metadata is invalid, or `--expect` does not match the evaluated
decision. A denied decision is otherwise a successful dry run and is printed as
`deny` with the policy reason. Use `--json` for CI assertions.

## HuggingFace ref forms

`model pull` accepts several ref forms for the `<hf-ref>` argument:

| Form | Example |
|---|---|
| `hf.co/<org>/<repo>/<file>.gguf` | `hf.co/TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf` |
| `huggingface.co/<org>/<repo>/<file>.gguf` | `huggingface.co/TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf` |
| Bare `<org>/<repo>/<file>.gguf` | `TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf` |
| Full `resolve` URL | `https://huggingface.co/TheBloke/Llama-2-7B-GGUF/resolve/main/llama-2-7b.Q4_K_M.gguf` |

An optional `@<rev>` suffix after the repository name pins a specific revision;
when omitted the ref resolves to `main`:

```text
TheBloke/Llama-2-7B-GGUF@abc123/llama-2-7b.Q4_K_M.gguf
```

## Authentication

`model pull` authenticates to HuggingFace using a bearer token. The token is
resolved in this order:

1. `--token <t>` flag
2. `HF_TOKEN` environment variable
3. `HUGGING_FACE_HUB_TOKEN` environment variable

If none is set, the pull is attempted without authentication. Public models do
not require a token.

## Store location

Downloaded blobs are stored in a content-addressed layout under the state
directory:

```text
~/.microagent/models/
  index.json           # ref → digest mapping
  blobs/
    <24-hex-chars>.gguf    # raw GGUF file, named by first 24 hex chars of sha256(canonical-ref)
```

`model delete` removes the index record and, unless `--keep-files` is set, deletes
the corresponding blob. `model prune` removes index records whose blob files
are missing from disk. With `--delete-files`, it also deletes the blob file of
every remaining indexed record (i.e. all indexed blobs are deleted).

## Flags

Most subcommands take only `--state-dir <dir>` (state directory, default
`~/.microagent/`); the flags that change behavior are `--token` (pull/serve),
`--keep-files` (delete), `--delete-files` (prune), `--dedicated` (serve), and
the host runner flags for `serve`.

### Pull flags

| Flag | Description |
|---|---|
| `--token <t>` | HuggingFace bearer token (falls back to `HF_TOKEN`, then `HUGGING_FACE_HUB_TOKEN`) |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |

### Serve flags

| Flag | Description |
|---|---|
| `--dedicated` | Start a dedicated runner instead of reusing a shared one |
| `--runner-command <template>` | Custom host model runner command template |
| `--runner-name <name>` | Name to record for a custom host model runner |
| `--runner-health-path <path>` | HTTP health path for a custom host model runner |
| `--runner-arg <arg>` | Extra host model runner argument. Repeat for multiple argv entries |
| `--runner-env KEY=VALUE` | Extra host model runner environment override. Repeat for multiple variables |
| `--token <t>` | HuggingFace bearer token used if the model must be auto-pulled |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |

### Remove flags

| Flag | Description |
|---|---|
| `--keep-files` | Remove the index record but keep the blob file on disk |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |

### Prune flags

| Flag | Description |
|---|---|
| `--delete-files` | Also delete the blob files of all indexed models (not just orphaned/missing ones) |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Exit status

`model` subcommands exit `0` on success; nonzero when a ref cannot be parsed, a
download or authentication fails, a record is not found, or `serve` cannot
start the selected host model runner. In AX mode a failure is written as a
structured error envelope.

## Related

- [`serve`](/cli/serve/) - MCP stdio endpoint
- [`run`](/cli/run/) - pair a one-shot run with a served model via `--model`
- [`create`](/cli/create/) - pair a workspace persistently via `--model`
- [`image`](/cli/image/) - the equivalent store for OCI images
- [`secret`](/cli/secret/) - deliver tokens to guests without writing them to disk
