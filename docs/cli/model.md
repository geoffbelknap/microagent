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
```

`model` manages a local content-addressed store of GGUF model files. Downloaded
blobs are stored under `~/.microagent/models/` by default, indexed by the
HuggingFace reference used to pull them. All four subcommands read and write this
index; no remote state is modified.

## Commands

| Command | Description |
|---|---|
| `pull` | Download a GGUF file from HuggingFace and record it in the local store |
| `ls` | List locally stored model records (ref, size, digest) |
| `rm` | Remove a model record, and optionally delete its blob |
| `prune` | Remove records whose blob files are missing; optionally delete orphaned blobs |

`ls` is also available as `list`.

`rm` is also available as `remove` and `delete`.

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
    sha256-<digest>    # raw GGUF file
```

`model rm` removes the index record and, unless `--keep-files` is set, deletes
the corresponding blob. `model prune` removes records whose blob file no longer
exists on disk; with `--delete-files` it also deletes blobs that are not
referenced by any remaining index record.

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
| `--delete-files` | Also delete blob files for pruned entries |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |

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

# Remove records for missing blobs and also delete orphaned blobs
microagent model prune --delete-files
```

`model ls` prints one row per recorded model:

```text
MODEL                                                              SIZE        DIGEST
TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf                  3825819648  sha256:abc...
```

With the global `--json` flag, records are returned under `models`:

```json
{
  "models": [
    {
      "model_ref": "TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf",
      "digest": "sha256:abc...",
      "size_bytes": 3825819648,
      "blob_path": "/home/user/.microagent/models/blobs/sha256-abc..."
    }
  ]
}
```

## Related

- [`images`](/cli/images/)
- [`secret`](/cli/secret/)
