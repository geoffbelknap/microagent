---
title: microagent model
description: Download and manage local HuggingFace GGUF model files.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-03_

```text
microagent model pull <hf-ref> [--token <t>] [--state-dir <dir>]
microagent model ls [--state-dir <dir>]
microagent model rm <ref> [--keep-files] [--state-dir <dir>]
microagent model prune [--delete-files] [--state-dir <dir>]
microagent model serve <hf-ref> [--dedicated] [--token <t>] [--state-dir <dir>]
microagent model stop <hf-ref> [--state-dir <dir>]
microagent model runners [--state-dir <dir>]
```

`model` manages a local content-addressed store of GGUF model files and the
host model server processes that serve them. Downloaded blobs are stored under
`~/.microagent/models/` by default, indexed by the HuggingFace reference used
to pull them. All subcommands read and write this index; no remote state is
modified by the store commands. The server commands (`serve`, `stop`, `runners`)
manage long-running `llama-server` processes on the host.

## Commands

| Command | Description |
|---|---|
| `pull` | Download a GGUF file from HuggingFace and record it in the local store |
| `ls` | List locally stored model records (ref, size, digest) |
| `rm` | Remove a model record, and optionally delete its blob |
| `prune` | Remove records whose blob files are missing; with `--delete-files`, also delete all indexed blob files |
| `serve` | Start (or reuse) a pinned host model server for a model; auto-pulls if not yet stored |
| `stop` | Force-stop all model server processes for a model ref |
| `runners` | List currently running model server processes |

`ls` is also available as `list`.

`rm` is also available as `remove` and `delete`.

`runners` is also available as `ps` (within the `model` subcommand).

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

`model rm` removes the index record and, unless `--keep-files` is set, deletes
the corresponding blob. `model prune` removes index records whose blob files are
missing from disk. With `--delete-files`, it also deletes the blob file of every
remaining indexed record (i.e. all indexed blobs are deleted).

## Pull flags

| Flag | Description |
|---|---|
| `--token <t>` | HuggingFace bearer token (falls back to `HF_TOKEN`, then `HUGGING_FACE_HUB_TOKEN`) |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |

## List flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |

## Remove flags

| Flag | Description |
|---|---|
| `--keep-files` | Remove the index record but keep the blob file on disk |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |

## Prune flags

| Flag | Description |
|---|---|
| `--delete-files` | Also delete the blob files of all indexed models (not just orphaned/missing ones) |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |

## Serve flags

| Flag | Description |
|---|---|
| `--dedicated` | Start a dedicated runner for this caller instead of reusing a shared one |
| `--token <t>` | HuggingFace bearer token used if the model must be auto-pulled |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |

`serve` requires `llama-server` on PATH or set via the `MICROAGENT_LLAMA_SERVER`
environment variable. If neither is present, the command exits with a clear error
message rather than panicking.

If the model is not yet in the local store, `serve` pulls it automatically before
starting the server (equivalent to running `model pull` first). The runner is
started with `--pinned`, so it stays alive even when no workspace holds it.

## Stop flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |

`stop` force-stops all model server processes for the given ref (ignores whether
the runner is pinned) and removes their entries from the runner index.

## Runners flags

| Flag | Description |
|---|---|
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |

`runners` self-heals the registry: any listed process that is no longer alive is
silently removed before the list is printed.

## Examples

```bash
# Download a public model
microagent model pull TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf

# Download a gated model with an explicit token
microagent model pull hf.co/meta-llama/Llama-2-7B/llama-2-7b.gguf --token hf_xxxxx

# Download using an environment variable for the token
HF_TOKEN=hf_xxxxx microagent model pull TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf

# List stored models
microagent model ls

# Remove a model and delete its blob
microagent model rm TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf

# Remove the index entry but leave the blob file on disk
microagent model rm TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf --keep-files

# Remove records for missing blobs (safe; no files deleted)
microagent model prune

# Remove records for missing blobs and also delete blob files of all indexed models
microagent model prune --delete-files

# Start a shared pinned model server (auto-pulls if not stored)
microagent model serve TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf

# Start a dedicated runner (exclusive to this caller)
microagent model serve TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf --dedicated

# Stop all runners for a model
microagent model stop TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf

# List running model servers
microagent model runners
```

`model ls` prints one tab-separated row per recorded model (no header):

```text
hf.co/TheBloke/Llama-2-7B-GGUF@main/llama-2-7b.Q4_K_M.gguf	3825819648	sha256:abc...
```

With the global `--json` flag, records are returned under `models`:

```json
{
  "models": [
    {
      "model_ref": "hf.co/TheBloke/Llama-2-7B-GGUF@main/llama-2-7b.Q4_K_M.gguf",
      "digest": "sha256:abc...",
      "size_bytes": 3825819648,
      "output_path": "/home/user/.microagent/models/blobs/a1b2c3d4e5f6a7b8c9d0e1f2.gguf"
    }
  ]
}
```

## Related

- [`images`](/cli/images/)
- [`secret`](/cli/secret/)
