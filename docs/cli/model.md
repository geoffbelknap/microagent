---
title: microagent model
description: Download and manage local HuggingFace GGUF model files.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-23_

```text
microagent model pull <hf-ref> [--token <t>] [--state-dir <dir>]                  Download a GGUF model
microagent model list [--state-dir <dir>]                                           List stored models
microagent model delete <ref> [--keep-files] [--state-dir <dir>]                      Remove a model and its blob
microagent model prune [--delete-files] [--state-dir <dir>]                       Drop records for missing blobs
microagent model serve <hf-ref> [--dedicated] [--runner <llamacpp|vllm|custom>]   Serve a model on the host
                       [--runner-gpu <off|on|auto>] [--runner-model <id>]
                       [--runner-served-model <name>] [--runner-command <template>]
                       [--runner-name <name>] [--runner-health-path <path>]
                       [--runner-arg <arg>] [--runner-env KEY=VALUE]
                       [--token <t>] [--state-dir <dir>]
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

`model list` prints one tab-separated row per recorded model (no header).
The digest column shows a short, 12-character form of the hash (the
algorithm prefix is dropped); the full digest is always available with
`--json`:

```text
hf.co/TheBloke/Llama-2-7B-GGUF@main/llama-2-7b.Q4_K_M.gguf	3825819648	abc123def456
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
| `policy evaluate` | Dry-run a policy file against structured request metadata (`policy eval` is an accepted alias) |

`serve` supports three runner backends. `llamacpp` is the default and uses
`llama-server` with CPU execution unless GPU use is explicitly requested.
`vllm` starts vLLM's OpenAI-compatible API server and requires
`--runner-model <hf-model-id>` plus `MICROAGENT_VLLM_PYTHON` when vLLM is not
available through `python3`. `custom` runs an operator-supplied
OpenAI-compatible command template. If the model is not yet in the local GGUF
store, `serve` pulls it automatically before starting the server. The runner is
started pinned, so it stays alive even when no workspace holds it.

microagent starts the default llama.cpp runner with `--device none
--gpu-layers 0`. Pointing `MICROAGENT_LLAMA_SERVER` at a CUDA-enabled binary is
therefore not enough to opt into GPU use. Pass `--runner-gpu on`, `--runner-gpu
auto`, or explicit llama.cpp GPU args such as `--runner-arg -ngl --runner-arg
all`.

Use the named vLLM backend when the host should run vLLM rather than
llama.cpp:

```bash
MICROAGENT_VLLM_PYTHON=../vllm/.venv/bin/python \
  microagent model serve org/stub/stub.gguf \
  --runner vllm \
  --runner-model Qwen/Qwen2.5-0.5B-Instruct \
  --runner-served-model local-chat \
  --runner-arg --max-model-len --runner-arg 2048
```

The `<hf-ref>` argument remains the microagent model-store pairing ref. vLLM
loads the backend model named by `--runner-model`, and clients send the
`--runner-served-model` value when it is set.

Set `MICROAGENT_MODEL_RUNNER_COMMAND` or pass `--runner-command` to use a custom
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
Workspace `--model-runner-env` values apply only to the current `run`,
`create`, or `start` invocation and are not persisted in workspace manifests.

Workspaces hold runners. `run --model` holds one for the duration of the run.
A workspace created with `create --model` re-pairs on every `start` and holds
until `halt` (or its `stop` alias), `kill`, or `delete` releases it - a guest
that exits on its own keeps its hold until the next lifecycle verb. An unpinned runner stops
when its last holder releases; a pinned one (`model serve`) stays up.
Runner backend, GPU intent, command template, args, and model mediation config
from `create --model` are persisted so `start` and `supervise` replay the same
pairing. `start` accepts the same `--model-runner*` and `--model-mediation*`
flags as overrides for a single boot.
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

## Model mediation policy files

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
owns the fail-closed host enforcement around that decision.

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
`deny` with the policy reason. Use `--json` for automation.

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
| `--runner <backend>` | Runner backend: `llamacpp` (default), `vllm`, or `custom` |
| `--runner-gpu <mode>` | Runner GPU intent: `off` (llama.cpp default), `on`, or `auto` |
| `--runner-model <id>` | Backend model id for runners such as vLLM |
| `--runner-served-model <name>` | OpenAI-compatible served model name for runners such as vLLM |
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

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--mode`.

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
