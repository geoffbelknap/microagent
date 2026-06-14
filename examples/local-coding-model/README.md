# local-coding-model

This example runs a local GGUF coding model through microagent's model runner.
microagent starts `llama-server` on the host, pairs a workspace to that runner,
and injects `OPENAI_BASE_URL` into the guest. The llama.cpp web UI is available
on the same host runner URL.

The workspace itself is deliberately small and one-shot: it boots Python, sends
one coding prompt to the paired local model, writes the response to
`/workspace/result.json`, and exits. After it exits, `microagent list` should
show `local-coding-model` as `stopped`. If the llama.cpp web UI is still open,
you are using the host model runner, not a running VM.

## Files

| File | Role |
|---|---|
| `microagent.yaml` | Workspace recipe with the local model pairing and guest smoke-test client. |
| `ask_model.py` | Guest script that calls the injected OpenAI-compatible endpoint. |
| `webui-url.sh` | Host helper that prints the llama.cpp web UI URL for the running model. |

## Prerequisites

- `llama-server` on `PATH`, or `MICROAGENT_LLAMA_SERVER` pointing at the binary.
- Enough disk and memory for the selected GGUF model.
- Network access for the first `microagent model pull`, unless the model is
  already in the microagent model store.

The default model is intentionally small enough for local iteration:

```text
Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf
```

You can replace the `model:` field in `microagent.yaml` with any GGUF Hugging
Face ref supported by `microagent model pull`.

## Run

From the repo root:

```sh
microagent model pull \
  Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf

microagent create --file examples/local-coding-model/microagent.yaml
microagent start local-coding-model
```

Print the llama.cpp web UI URL for the model runner:

```sh
examples/local-coding-model/webui-url.sh
```

Open that URL in a browser. The same host runner serves the web UI and the
OpenAI-compatible API used by the guest. The VM may already be stopped by this
point because the example guest does one request and exits.

To run only the llama.cpp web UI without starting the example VM, use a pinned
model runner:

```sh
microagent model serve \
  Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf

examples/local-coding-model/webui-url.sh
```

Fetch the workspace result after the guest exits:

```sh
microagent cp local-coding-model:/workspace/result.json \
  examples/local-coding-model/result.json
```

## Stop

```sh
microagent halt local-coding-model
microagent model stop \
  Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf
```

## Notes

- `microagent create` persists the model ref, so later `start` calls re-pair the
  workspace with a host runner.
- `microagent model serve <ref>` starts a pinned runner without a workspace.
  That is useful when you only want the llama.cpp web UI.
- `microagent list` reports VM state, not model runner state. Use
  `microagent model runners` to inspect host model servers.
- The model server is host-local. The guest reaches it through microagent's
  model forwarding path instead of a broad host filesystem or network mount.
