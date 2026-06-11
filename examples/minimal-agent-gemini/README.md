# minimal-agent-gemini

The Google Gemini variant of [`minimal-agent`](../minimal-agent/). Same agent protocol, same `bash` / `read_file` / `write_file` tools, same `/workspace` boundary - only the model and SDK differ. Uses the `google-genai` SDK with function calling (`gemini-2.5-flash` by default; swap the model string in `agent.py` to use a different one).

This example uses Google's newer `google-genai` SDK. The older
`google-generativeai` package still works for some patterns, but use
`google-genai` for this example.

## Files

| File | Role |
|---|---|
| `microagent.yaml` | Workspace spec - image, deps (`google-genai`), entrypoint, source files, outputs. |
| `protocol.py` | Pydantic v2 models for the agent protocol (identical to the Anthropic variant). |
| `agent.py` | The agent - Gemini chat session with bash/read_file/write_file function declarations. |
| `demo/` | Operator-side files: constraints, system prompt, and a library of example requests (see [`demo/README.md`](demo/README.md)). |

## Run

You'll need a Gemini API key as `GEMINI_API_KEY` (get one at [aistudio.google.com](https://aistudio.google.com)). The flow mirrors the [simple-agent recipe](../../docs/guides/simple-agent.md), with two substitutions:

```bash
microagent create \
  --file examples/minimal-agent-gemini/microagent.yaml \
  --env GEMINI_API_KEY=$GEMINI_API_KEY

microagent cp examples/minimal-agent-gemini/demo/input-001.json minimal-agent-gemini:/workspace/input.json
microagent start minimal-agent-gemini
microagent --json result minimal-agent-gemini
```

Everything else - halt, resume, retrieve files, clean up - is identical to the Anthropic recipe.

## Note on SDK churn

The `google-genai` SDK is newer and the function-calling surface has evolved across releases. If you see import errors or shape mismatches after upgrading the SDK, check Google's current Python SDK docs and adjust `agent.py` accordingly - the protocol shapes don't change, only how Gemini wants the function declarations and responses presented.
