---
title: Build a simple agent
description: Boot a microVM, point it at Claude, watch it write and run files in its own workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

This guide builds a small agent in a Linux microVM. The body calls Claude with
`bash`, `read_file`, and `write_file` tools, so Claude can edit code, run
commands, and inspect files inside `/workspace`. Halt the workspace, swap in a
new prompt, start it back up, and Claude can read whatever it wrote on the
previous run.

New here? Start with [run your first agent](/getting-started/cli/first-agent/)
for the quickstart version. This guide spends more time on the body, prompt
caching, and the gaps between this demo and a production setup.

[`examples/minimal-body/microagent.yaml`](https://github.com/geoffbelknap/microagent/tree/main/examples/minimal-body/microagent.yaml)
describes the workspace. One spec file, one `microagent create` call, no
separate build step.

## What you'll need

- microagent installed and `microagent doctor` passing - see [install](/getting-started/install/).
- On Linux, `pasta` for the default unprivileged network mode. Homebrew installs it as a microagent dependency; on apt-based distros it's `sudo apt install passt`, on Fedora it's `sudo dnf install passt`.
- On macOS the default backend is Apple Virtualization.framework (no `pasta` needed), but the rootfs builder needs `mke2fs` - `brew install e2fsprogs` and pass `--mke2fs` the first time. See [troubleshooting](/troubleshooting/#mke2fs-not-found-rootfs-builds-fail).
- An Anthropic API key in `ANTHROPIC_API_KEY`. Sign up at [console.anthropic.com](https://console.anthropic.com) if you don't have one.

## Step 1 - create the workspace

From the repo root:

```bash
microagent create \
  --file examples/minimal-body/microagent.yaml \
  --env ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY
```

The spec sets the workspace name to `minimal-body`; the rest of these commands
use that name. The first create takes a minute or so because microagent pulls
the OCI base image, builds the rootfs, and runs the `setup:` commands that
install Pydantic and the Anthropic SDK.

The spec file pulls a stock `python:3.13-slim` image, installs `pydantic` and
`anthropic` via `setup`, copies the body source and operator files into the
rootfs via `files:`, sets the entrypoint, and declares the result artifact. The
CLI adds the API key as an env var, so host secrets stay out of the spec.

The body's `process()` function sends the request to Claude with the three
tools, runs tool calls inside `/workspace`, feeds results back, and loops until
Claude returns a final answer. Prompt caching is on by default. The system
prompt is stable across requests, so the body pays for it once and reads it
back at about 10x cheaper afterward.

The full body source is in [`examples/minimal-body/body.py`](https://github.com/geoffbelknap/microagent/tree/main/examples/minimal-body/body.py).

## Step 2 - deliver the request

The spec covers everything that doesn't change between runs. The one thing that changes per run is the request itself, delivered with `microagent cp`:

```bash
microagent cp examples/minimal-body/demo/input-001.json minimal-body:/workspace/input.json
```

The first request asks for something concrete:

```json
{
  "request_id": "req-001",
  "content": "Create a Python script at /workspace/hello.py that prints 'hello from a microVM' on one line and the running Linux kernel version (use uname -r) on the next. Run it and show me the output.",
  ...
}
```

(Full file: [`examples/minimal-body/demo/input-001.json`](https://github.com/geoffbelknap/microagent/tree/main/examples/minimal-body/demo/input-001.json).)

The system prompt - already baked into the workspace by the spec - makes the agent take initiative:

```text
You are an agent running inside a Linux microVM. You have access to a workspace
at /workspace where you can run shell commands, read files, and write files
using your tools.

Help the user with their request - actually do the work, don't just describe
it. When you're finished, briefly summarize what you accomplished and where
the user can find the results.
```

## Step 3 - run and look at what happened

```bash
microagent start minimal-body
microagent --json status minimal-body   # poll until the result is ready
microagent --json result minimal-body
```

The body usually takes 5-10 seconds: the VM boots, the body emits `ready`, runs
the structural checks, calls Claude, writes the result, and exits. `result`
reads the result file as it stands, so run it after the body has finished -
`microagent --json status minimal-body` includes the structured `result` once
it's ready and reports `stopped` after the body exits. Claude's final summary
appears in the `content` field. It should look something like:
*"I created `/workspace/hello.py`, ran it with `python3`, and got `hello from a
microVM` followed by the kernel version `6.1.x`."*

The file Claude wrote is still on the workspace's disk. Pull it out:

```bash
microagent cp minimal-body:/workspace/hello.py ./hello.py
cat ./hello.py
```

That is the script Claude wrote, retrieved from the microVM.

## Step 4 - halt, ask a follow-up, resume

The workspace persists between starts: disk, files, all of it. Halt cleanly,
deliver a new request, and start it back up. Claude can read whatever it wrote
on the previous run.

```bash
microagent halt minimal-body
microagent cp examples/minimal-body/demo/input-002.json minimal-body:/workspace/input.json
microagent start minimal-body
microagent --json result minimal-body
```

The second request asks Claude to read `/workspace/hello.py` and explain it.
The file is still there from the first run. The system prompt and installed
deps are still there too. Anthropic's prompt cache is still warm, so the second
request reads the system prompt back at about 10x cheaper than the first paid
for it.

(See [glossary](/concepts/glossary/) for halt vs stop vs kill vs quarantine.)

## Step 5 - clean up

```bash
microagent halt minimal-body
microagent delete minimal-body
```

`delete` removes the workspace record and disk. (For Firecracker, `delete` refuses while the VM is still running; halt or stop first.)

## Try it with another provider

The body shape does not depend on which model it talks to. Sibling examples run
the same flow against OpenAI and Gemini with the same protocol, tools,
workspace, and walkthrough. Each variant has its own `microagent.yaml` and README:

- [`examples/minimal-body-openai/`](https://github.com/geoffbelknap/microagent/tree/main/examples/minimal-body-openai) - OpenAI Chat Completions with function calling.
- [`examples/minimal-body-gemini/`](https://github.com/geoffbelknap/microagent/tree/main/examples/minimal-body-gemini) - Google Gemini with function calling.

Swap the spec path and the API-key env var; everything else stays the same.

## What this isn't yet

This guide runs the agent against one request per restart and uses an env-var API key. Two production-shape gaps:

- **One request per restart.** A real deployment streams `WorkRequest`/`WorkResult` over the mediation channel instead of `microagent cp` - see [build agents on the mediation channel](/guides/agents-and-mediation/).
- **The body holds the key.** Passing `ANTHROPIC_API_KEY` (or `OPENAI_API_KEY`, `GEMINI_API_KEY`) as an env var means the body reaches the model directly. The production shape routes the call through a host-side proxy that holds the key, audits requests, and forwards them. See [agency](https://github.com/geoffbelknap/agency) for an implementation.

## What to read next

- [`microagent.yaml`](/cli/spec/) - the full workspace spec reference.
- [Glossary](/concepts/glossary/) - workspace, mediation, halt vs quarantine, etc.
- [State and identity](/concepts/state-and-identity/) - how lifecycle events are emitted and what `microagent --json status` reports.
- [`examples/minimal-body/`](https://github.com/geoffbelknap/microagent/tree/main/examples/minimal-body) - the body source.
