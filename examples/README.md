# Examples

Each directory is a runnable microagent project. If you're brand new, the
[first agent walkthrough](../docs/getting-started/cli/first-agent.md) is the
guided version of `minimal-agent`.

| Example | What it shows | Start with |
|---|---|---|
| [`minimal-agent/`](minimal-agent/) | A small LLM agent (Anthropic) with bash/read/write tools running inside a microVM. The checked-in output of `microagent init` - scaffold your own copy for any provider with `microagent init <name> --provider anthropic\|openai\|gemini`. | [simple-agent guide](../docs/guides/simple-agent.md) |
| [`agents/`](agents/) | Build-free Agentfiles for `microagent dispatch`: OpenAI Agents SDK and Claude agents run one-shot against a broker endpoint - the key is injected host-side and the guest never holds it. | [`agents/README.md`](agents/README.md) |
| [`local-coding-model/`](local-coding-model/) | A workspace paired with a local GGUF model served by `llama-server` on the host - no API key, no cloud. | [`local-coding-model/README.md`](local-coding-model/README.md) |
| [`homebridge/`](homebridge/) | A long-running service workload: setup script, supervised `service:` command, `restart: always`, and a published port. | [`homebridge/README.md`](homebridge/README.md) |

Every example is a plain `microagent.yaml` (or Agentfile) plus source files -
nothing here uses APIs your own projects can't. The spec format is documented
in [`microagent.yaml`](../docs/cli/spec.md).
