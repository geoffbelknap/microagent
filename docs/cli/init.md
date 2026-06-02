---
title: microagent init
description: Scaffold a starter agent body project.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-02_

```text
microagent init <name> [options]
```

`init` scaffolds a starter agent body: a `microagent.yaml` spec, a
provider-specific `body.py`, the shared `protocol.py`, and a runnable demo
request. It turns the [`minimal-body`](https://github.com/geoffbelknap/microagent/tree/main/examples/minimal-body)
example into a one-command starting point. The generated project is consumed by
the normal [`create`](/cli/create/) / [`cp`](/cli/cp/) / [`start`](/cli/start/)
flow — `init` only writes files; it does not build or run anything.

It fails closed: if a target file already exists, `init` writes nothing unless
`--force` is set.

## Options

| Flag | Description |
|---|---|
| `--provider <name>` | Body provider: `anthropic` (default), `openai`, or `gemini`. Selects the SDK installed by the spec and the model the body calls. |
| `--dir <path>` | Target directory. Defaults to `./<name>`. |
| `--force` | Overwrite existing files instead of failing. |

The `--name` argument is the workspace name written into the spec and the
directory created for the project.

## Generated layout

```text
<name>/
  microagent.yaml      # workspace spec: image, setup, entrypoint, files, outputs
  body.py              # the agent body — the loop you edit
  protocol.py          # request/reply wire shapes (shaped by the ASK framework)
  README.md            # how to run and edit the project
  demo/
    constraints.json   # operator-owned constraint envelope
    system_prompt.md   # system prompt
    input-001.json     # a sample WorkRequest
```

## Example

```bash
# Scaffold an Anthropic agent in ./triage
microagent init triage

# Then create, deliver a request, and start it
cd triage
microagent create --file microagent.yaml --env ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY
microagent cp demo/input-001.json triage:/workspace/input.json
microagent start triage

# Other providers
microagent init scout --provider openai
microagent init helper --provider gemini
```

With `--json` (or AX mode), `init` prints a structured summary of the generated
project — name, provider, directory, API-key env var, and the file list.

## Related

- [`create`](/cli/create/)
- [`cp`](/cli/cp/)
- [`start`](/cli/start/)
- [`run`](/cli/run/)
