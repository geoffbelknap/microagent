# minimal-body (Anthropic)

A small agent body that runs inside a microVM, calls Claude with `bash`, `read_file`, and `write_file` tools, and lets Claude actually do work in its `/workspace`. Prompt caching is on by default - the system prompt is paid for once and read from cache afterward.

The whole workspace is described in [`microagent.yaml`](microagent.yaml). One spec, one `microagent create`.

For the walkthrough - create, deliver a request, run, halt-and-resume, retrieve files Claude wrote - see [`docs/recipes/simple-agent.md`](../../docs/recipes/simple-agent.md).

## Files

| File | Role |
|---|---|
| `microagent.yaml` | Workspace spec - image, deps, entrypoint, source files, output artifacts. |
| `protocol.py` | Pydantic v2 models for the body protocol (request, result, lifecycle signals). |
| `body.py` | The body - Claude tool-use loop with bash/read_file/write_file, scoped to /workspace. |
| `demo/` | Operator-side files (constraints, system prompt, two example requests). The first three are baked into the workspace by the spec; the request inputs are delivered per run. |

## Other providers

Sibling examples for the same body shape with different model providers:

- [`../minimal-body-openai/`](../minimal-body-openai/) - OpenAI Chat Completions with function calling.
- [`../minimal-body-gemini/`](../minimal-body-gemini/) - Google Gemini with function calling.

## Run

You'll need an Anthropic API key as `ANTHROPIC_API_KEY`. Follow [`docs/recipes/simple-agent.md`](../../docs/recipes/simple-agent.md).
