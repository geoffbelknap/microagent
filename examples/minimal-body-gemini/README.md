# minimal-body-gemini

The Google Gemini variant of [`minimal-body`](../minimal-body/). Same body protocol, same `bash` / `read_file` / `write_file` tools, same `/workspace` boundary - only the model and SDK differ. Uses the `google-genai` SDK with function calling (`gemini-2.5-flash` by default; swap the model string in `body.py` to use a different one).

This example uses Google's newer `google-genai` SDK. The older
`google-generativeai` package still works for some patterns, but use
`google-genai` for this example.

## Files

| File | Role |
|---|---|
| `microagent.yaml` | Workspace spec - image, deps (`google-genai`), entrypoint, source files, outputs. |
| `protocol.py` | Pydantic v2 models for the body protocol (identical to the Anthropic variant). |
| `body.py` | The body - Gemini chat session with bash/read_file/write_file function declarations. |
| `demo/` | Operator-side files (constraints, system prompt, two example requests). |

## Run

You'll need a Gemini API key as `GEMINI_API_KEY` (get one at [aistudio.google.com](https://aistudio.google.com)). The flow mirrors the [simple-agent recipe](../../docs/recipes/simple-agent.md), with two substitutions:

```bash
microagent create \
  --file examples/minimal-body-gemini/microagent.yaml \
  --env GEMINI_API_KEY=$GEMINI_API_KEY

microagent cp examples/minimal-body-gemini/demo/input-001.json minimal-body-gemini:/workspace/input.json
microagent start minimal-body-gemini
microagent --json result minimal-body-gemini
```

Everything else - halt, resume, retrieve files, clean up - is identical to the Anthropic recipe.

## Note on SDK churn

The `google-genai` SDK is newer and the function-calling surface has evolved across releases. If you see import errors or shape mismatches after upgrading the SDK, check Google's current Python SDK docs and adjust `body.py` accordingly - the protocol shapes don't change, only how Gemini wants the function declarations and responses presented.
