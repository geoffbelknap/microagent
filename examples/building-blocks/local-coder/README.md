# local-coder

**Aha:** run a coding agent against a model on *your own machine* — no API key,
no cloud provider — and let it fix failing tests inside a throwaway microVM.

`microagent --model` serves a local GGUF model with `llama-server` on the host
and injects `OPENAI_BASE_URL` into the guest. So the agent ([`agent.py`](agent.py),
~60 lines) is just a stock OpenAI client pointed at a model running on your
laptop. Your code and your prompts never go to a third-party API.

## Files

| File | Role |
|---|---|
| `microagent.yaml` | Workspace spec — image, deps, the agent, the task files, and the output artifacts. |
| `agent.py` | The agent. Reads the task, asks the local model for a corrected file, runs `pytest`, loops on the failures. |
| `task/` | A buggy `calculator.py`, its `test_calculator.py`, and a `TASK.md` describing the goal. |

## Run

You need `llama-server` on your `PATH` (from
[`llama.cpp`](https://github.com/ggml-org/llama.cpp)); `microagent` finds it
there or via `MICROAGENT_LLAMA_SERVER`.

```bash
# 1. Pull a small local model once (any GGUF works).
microagent model pull unsloth/Qwen3-4B-Instruct-2507-GGUF/Qwen3-4B-Instruct-2507-Q4_K_M.gguf

# 2. Build the workspace and pair it with the local model.
microagent create --file microagent.yaml \
  --model unsloth/Qwen3-4B-Instruct-2507-GGUF/Qwen3-4B-Instruct-2507-Q4_K_M.gguf

# 3. Boot it. The agent runs as the entrypoint and exits when the tests pass.
microagent start local-coder

# 4. Pull the result out of the stopped workspace.
microagent artifact get local-coder result ./out/
microagent artifact get local-coder calculator ./out/
cat ./out/result.json
```

`result.json` records whether the tests passed and what happened each round;
`calculator.py` is the model's fixed version. Round-by-round progress streams
to the workspace logs (`microagent logs local-coder`).

## Make it yours

- **Swap the model.** Any GGUF reference works — point `--model` at a coder-tuned
  model, a bigger quant, or whatever you already have stored (`microagent model list`).
- **Swap the task.** Replace `task/` with your own failing tests and a `TASK.md`.
  The agent doesn't know anything calculator-specific; it just makes `pytest` pass.
- **Prove it can't phone home.** This block keeps the network at its default so the
  guest can reach the host model endpoint. Pair the same pattern with an
  egress-locked network (see the [networking guide](../../../docs/guides/networking.md))
  when you want "the agent edited my source and *nothing* could leave the box."

## Why a microVM

The agent runs untrusted, model-written code (`pytest` executes whatever the
model wrote into `calculator.py`). Here that's a toy, but the shape is the real
one: generated code runs in an isolated microVM with its own kernel and rootfs,
so a bad edit can't touch your machine, and teardown is a `delete`.
