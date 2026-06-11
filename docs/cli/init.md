---
title: microagent init
description: Scaffold a starter agent project.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

```text
microagent init <name> [options]
```

`init` scaffolds a starter agent: a `microagent.yaml` spec, a
provider-specific `agent.py`, the shared `protocol.py`, and runnable demo
requests. It turns the [`minimal-agent`](https://github.com/geoffbelknap/microagent/tree/main/examples/minimal-agent)
example into a one-command starting point. The generated project is consumed by
the normal [`create`](/cli/create/) / [`cp`](/cli/cp/) / [`start`](/cli/start/)
flow - `init` only writes files; it does not build or run anything.

It fails closed: if a target file already exists, `init` writes nothing unless
`--force` is set.

## Examples

Scaffold an Anthropic agent, then create, deliver a request, and start it:

```bash
microagent init triage
cd triage
microagent create --file microagent.yaml --env ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY
microagent cp demo/input-001.json triage:/workspace/input.json
microagent start triage
```

Scaffold for other providers:

```bash
microagent init scout --provider openai
microagent init helper --provider gemini
```

With the global `--json` flag (or AX mode), `init` prints a structured summary
of the generated project - name, provider, directory, API-key env var, and the
file list.

## Generated layout

```text
<name>/
  microagent.yaml      # workspace spec: image, setup, entrypoint, files, outputs
  agent.py             # the agent - the loop you edit
  protocol.py          # request/reply wire shapes (shaped by the ASK framework)
  README.md            # how to run and edit the project
  demo/
    constraints.json   # operator-owned constraint envelope
    system_prompt.md   # system prompt
    input-001.json     # first WorkRequest: install rich, render a file-size table
    input-002.json     # follow-up WorkRequest for the halt/resume flow
```

The `<name>` argument is the workspace name written into the spec and the
directory created for the project.

## Flags

You'll rarely need flags here - `--provider` to pick the SDK and model the
generated agent calls.

| Flag | Description |
|---|---|
| `--provider <name>` | Agent provider: `anthropic` (default), `openai`, or `gemini`. Selects the SDK installed by the spec and the model the agent calls |
| `--dir <path>` | Target directory. Defaults to `./<name>` |
| `--force` | Overwrite existing files instead of failing |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Exit status

`init` exits `0` when the project is written; nonzero when a target file
already exists (without `--force`) or a file cannot be written. In AX mode a
failure is written as a structured error envelope.

## Related

- [`create`](/cli/create/) - build the workspace from the generated spec
- [`cp`](/cli/cp/) - deliver a work request into the workspace
- [`start`](/cli/start/) - boot the agent
- [Build a simple agent](/guides/simple-agent/) - the full walkthrough
