# minimal-agent (Anthropic)

A small agent that runs inside a microVM, calls Claude with `bash`, `read_file`, and `write_file` tools, and lets Claude actually do work in its `/workspace`. Prompt caching is on by default - the system prompt is paid for once and read from cache afterward.

The whole workspace is described in [`microagent.yaml`](microagent.yaml). One spec, one `microagent create`.

For the walkthrough - create, deliver a request, run, halt-and-resume, retrieve files Claude wrote - see [`docs/guides/simple-agent.md`](../../docs/guides/simple-agent.md).

## Files

| File | Role |
|---|---|
| `microagent.yaml` | Workspace spec - image, deps, entrypoint, source files, output artifacts. |
| `protocol.py` | Pydantic v2 models for the agent protocol (request, result, lifecycle signals). |
| `agent.py` | The agent - Claude tool-use loop with bash/read_file/write_file, scoped to /workspace. |
| `demo/` | Operator-side files: constraints, system prompt, and a library of example requests (see [`demo/README.md`](demo/README.md)). Constraints and the system prompt are baked into the workspace by the spec; request inputs are delivered per run. |

## Other providers

Sibling examples for the same agent shape with different model providers:

- [`../minimal-agent-openai/`](../minimal-agent-openai/) - OpenAI Chat Completions with function calling.
- [`../minimal-agent-gemini/`](../minimal-agent-gemini/) - Google Gemini with function calling.

## Run

You'll need an Anthropic API key as `ANTHROPIC_API_KEY`. Follow [`docs/guides/simple-agent.md`](../../docs/guides/simple-agent.md).
