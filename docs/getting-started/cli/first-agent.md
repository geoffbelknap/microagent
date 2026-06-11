---
title: Run your first agent
description: Boot a microVM, point it at an LLM, watch it write and run files in its own workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

Run an agent inside a microVM: a small body that calls an LLM with `bash`,
`read_file`, and `write_file` tools, does real work in its own workspace, and
reports a structured result. The example ships in three flavors: Anthropic
Claude, OpenAI, and Google Gemini. The flow is identical; only the example
folder and the API key env var change.

*If you just want to see microagent boot a microVM and run a command, start
with the [quickstart](/getting-started/quickstart/).*

## Before you start

1. [Install microagent](/getting-started/install/) and run `microagent doctor`.
2. Pick a provider and set the matching API key:

   | Provider | Example folder | API key env var | Sign up |
   |---|---|---|---|
   | Anthropic Claude | [`examples/minimal-body`](https://github.com/geoffbelknap/microagent/tree/main/examples/minimal-body) | `ANTHROPIC_API_KEY` | [console.anthropic.com](https://console.anthropic.com) |
   | OpenAI | [`examples/minimal-body-openai`](https://github.com/geoffbelknap/microagent/tree/main/examples/minimal-body-openai) | `OPENAI_API_KEY` | [platform.openai.com](https://platform.openai.com) |
   | Google Gemini | [`examples/minimal-body-gemini`](https://github.com/geoffbelknap/microagent/tree/main/examples/minimal-body-gemini) | `GEMINI_API_KEY` | [aistudio.google.com](https://aistudio.google.com) |

3. Clone the microagent repo to get the example sources:

   ```bash
   git clone https://github.com/geoffbelknap/microagent.git
   cd microagent
   ```

   **Faster, no clone:** [`microagent init`](/cli/init/) scaffolds the same
   project anywhere, for any provider:

   ```bash
   microagent init my-agent --provider anthropic   # or openai, gemini
   cd my-agent
   ```

   The generated project uses the workspace name you pass (`my-agent` above)
   instead of `minimal-body`, and its `body.py`, `protocol.py`, and demo
   request are identical to the example. Adjust the commands below to your
   name and run from the generated directory (use `--file microagent.yaml`).

The rest of this page uses the **Anthropic** example. To follow along with
OpenAI or Gemini instead, swap `minimal-body` for `minimal-body-openai` or
`minimal-body-gemini` in every command, and use the matching API key env var.

## Create the workspace

```bash
microagent create \
  --file examples/minimal-body/microagent.yaml \
  --env ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY
```

`create` takes no `--name` here: the spec's `name:` field sets the workspace
name to `minimal-body`, and that's what the rest of the commands refer to.
First-time create takes a minute or two: microagent
pulls the base Python image, builds the rootfs, installs Pydantic and the
Anthropic SDK, and copies the body source in. The API key is passed in as an
env var so it stays out of the spec file.

## Send a request

The body reads requests from `/workspace/input.json`. Drop the first one in
with `microagent cp`:

```bash
microagent cp examples/minimal-body/demo/input-001.json minimal-body:/workspace/input.json
```

The request asks for a concrete task - write a Python script, run it, show
the output.

## Run it

```bash
microagent start minimal-body
microagent --json result minimal-body
```

The body boots, calls the LLM with `bash` / `read_file` / `write_file`
tools, runs the tool calls inside `/workspace`, and writes a `WorkResult` to
`/workspace/result.json` (declared as the `result` output artifact in the
spec). That file carries the LLM's summary in a top-level `content` field:

```json
{
  "request_id": "req-001",
  "status": "completed",
  "content": "I created /workspace/hello.py, ran it with python3, and got 'hello from a microVM' followed by the running kernel version.",
  "error": null,
  "completed_at": "2026-06-01T00:01:07.482190+00:00",
  "audit_ref": "audit://req-001"
}
```

The `content` string is the LLM's wording, so it varies run to run; the other
fields (`request_id`, `status`, `audit_ref`) echo the request. `microagent
--json result` reads this file and reports the run's exit code in its
`result.exitCode` field - a clean exit is `0`.

The file the LLM wrote is still on the workspace's disk. Pull it out:

```bash
microagent cp minimal-body:/workspace/hello.py ./hello.py
cat ./hello.py
```

The `/workspace/hello.py` path is the one the request in `input-001.json`
asked for - microagent doesn't dictate it.

## Halt, ask a follow-up, resume

The workspace persists between starts - disk, files, all of it. Halt cleanly,
drop in a new request, start again. The LLM can read whatever it wrote on the
previous run.

```bash
microagent halt minimal-body
microagent cp examples/minimal-body/demo/input-002.json minimal-body:/workspace/input.json
microagent start minimal-body
microagent --json result minimal-body
```

The second request asks the LLM to read `/workspace/hello.py` and explain it.
The file is still there from the first run.

## Clean up

```bash
microagent halt minimal-body
microagent delete minimal-body
```

`delete` removes the workspace record and its disk.

## What's next

- [Build a simple agent](/recipes/simple-agent/) - the same flow with more
  on the body's structure, prompt caching, and the production-shape gaps
  (mediation channel, host-side proxy for keys).
- [`microagent.yaml`](/cli/spec/) - the full workspace spec reference.
- [State and identity](/concepts/state-and-identity/) - what `microagent --json status` reports and how lifecycle events are emitted.
- [Glossary](/concepts/glossary/) - workspace, mediation, halt vs stop vs kill vs quarantine.
