# minimal-agent-openai

The OpenAI variant of [`minimal-agent`](../minimal-agent/). Same agent protocol, same `bash` / `read_file` / `write_file` tools, same `/workspace` boundary - only the model and SDK differ. Uses OpenAI's Chat Completions API with function calling (`gpt-4o-mini` by default; set the `OPENAI_MODEL` environment variable to use a different one).

OpenAI applies prompt caching automatically for prefixes ≥ 1024 tokens - no client-side configuration needed.

## Files

| File | Role |
|---|---|
| `microagent.yaml` | Workspace spec - image, deps (`openai` instead of `anthropic`), entrypoint, source files, outputs. |
| `protocol.py` | Pydantic v2 models for the agent protocol (identical to the Anthropic variant). |
| `agent.py` | The agent - OpenAI tool-use loop with bash/read_file/write_file, scoped to /workspace. |
| `demo/` | Operator-side files (constraints, system prompt, two example requests). |

## Run

You'll need an OpenAI API key as `OPENAI_API_KEY`. The flow mirrors the [simple-agent recipe](../../docs/guides/simple-agent.md), with two substitutions:

```bash
microagent create \
  --file examples/minimal-agent-openai/microagent.yaml \
  --env OPENAI_API_KEY=$OPENAI_API_KEY

microagent cp examples/minimal-agent-openai/demo/input-001.json minimal-agent-openai:/workspace/input.json
microagent start minimal-agent-openai
microagent --json result minimal-agent-openai
```

Everything else - halt, resume, retrieve files, clean up - is identical to the Anthropic recipe.
