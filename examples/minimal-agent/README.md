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

`microagent init <name> --provider openai` (or `gemini`) scaffolds the same
agent shape against OpenAI Chat Completions or Google Gemini function calling.

## Keep in sync

Apart from this README, this directory is the checked-in output of
`microagent init minimal-agent --provider anthropic`. If you change the
scaffold templates in `pkg/scaffold/templates/`, regenerate this example so
they don't drift (then restore this README, which the scaffold's generic one
overwrites):

```sh
microagent init minimal-agent --provider anthropic --dir examples/minimal-agent --force
git checkout -- examples/minimal-agent/README.md
```

## Run

You'll need an Anthropic API key as `ANTHROPIC_API_KEY`. Follow [`docs/guides/simple-agent.md`](../../docs/guides/simple-agent.md).
