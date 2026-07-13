---
title: Build a simple agent
description: Boot a microVM, point it at Claude, watch it write and run files in its own workspace.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-13_

This example builds a small agent in a Linux microVM. The agent calls Claude with
`bash`, `read_file`, and `write_file` tools, so Claude can edit code, run
commands, and inspect files inside `/workspace`. Halt the workspace, swap in a
new prompt, start it back up, and Claude can read whatever it wrote on the
previous run.

New here? Start with [run your first agent](/getting-started/cli/first-agent/)
for the quickstart version. This page spends more time on the agent itself,
prompt caching, and the choices you would change for a production setup.

[`examples/minimal-agent/microagent.yaml`](https://github.com/geoffbelknap/microagent/tree/main/examples/minimal-agent/microagent.yaml)
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
  --file examples/minimal-agent/microagent.yaml \
  --env ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY
```

The spec sets the workspace name to `minimal-agent`; the rest of these commands
use that name. The first create takes a minute or so because microagent pulls
the OCI base image, builds the rootfs, and runs the `setup:` commands that
install Pydantic and the Anthropic SDK.

The spec file pulls a stock `python:3.13-slim` image, installs `pydantic` and
`anthropic` via `setup`, copies the agent source and operator files into the
rootfs via `files:`, sets the entrypoint, and declares the result artifact. The
CLI adds the API key as an env var, so host secrets stay out of the spec.

The agent's `process()` function sends the request to Claude with the three
tools, runs tool calls inside `/workspace`, feeds results back, and loops until
Claude returns a final answer. Prompt caching is on by default. The system
prompt is stable across requests, so the agent pays for it once and reads it
back at about 10x cheaper afterward.

The full agent source is in [`examples/minimal-agent/agent.py`](https://github.com/geoffbelknap/microagent/tree/main/examples/minimal-agent/agent.py).

## Step 2 - deliver the request

The spec covers everything that doesn't change between runs. The one thing that changes per run is the request itself, delivered with `microagent cp`:

```bash
microagent cp examples/minimal-agent/demo/input-001.json minimal-agent:/workspace/input.json
```

The first request asks for something concrete:

```json
{
  "request_id": "req-001",
  "content": "Install the Python package 'rich' with pip. Then write /workspace/biggest.py: a script that uses rich to print a table of the 5 largest files under /usr with their sizes. Run it and include the rendered table in your summary.",
  ...
}
```

(Full file: [`examples/minimal-agent/demo/input-001.json`](https://github.com/geoffbelknap/microagent/tree/main/examples/minimal-agent/demo/input-001.json).)

The system prompt (already baked into the workspace by the spec) makes the agent take initiative:

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
microagent start minimal-agent --wait
microagent --json result minimal-agent
```

The run takes half a minute or so: the VM boots, the agent emits `ready`, runs
the structural checks, calls Claude through the tool loop, writes the result,
and exits. [`--wait`](/cli/wait/) blocks until the workspace reports
`stopped`, so `result` reads the finished result file. (`microagent --json
status minimal-agent` still gives the point-in-time view, including the
structured `result` once it's ready.) Claude's final summary appears in the `content` field: a note that
`rich` was installed, plus the rendered table of the five largest files
under `/usr`.

The script Claude wrote is still on the workspace's disk. Pull it out:

```bash
microagent cp minimal-agent:/workspace/biggest.py ./biggest.py
cat ./biggest.py
```

That is the script Claude wrote, retrieved from the microVM.

## Step 4 - halt, ask a follow-up, resume

The workspace persists between starts: disk, files, all of it. Halt cleanly,
deliver a new request, and start it back up. Claude can read whatever it wrote
on the previous run.

```bash
microagent halt minimal-agent
microagent cp examples/minimal-agent/demo/input-002.json minimal-agent:/workspace/input.json
microagent start minimal-agent --wait
microagent --json result minimal-agent
```

The second request asks Claude to read `/workspace/biggest.py` from the first
run and extend it to show each file's last-modified date. The file is still
there from the first run. The system prompt and installed
deps are still there too. Anthropic's prompt cache is still warm, so the second
request reads the system prompt back at about 10x cheaper than the first paid
for it.

(See [glossary](/concepts/glossary/) for halt vs stop vs kill vs quarantine.)

## Step 5 - clean up

```bash
microagent halt minimal-agent
microagent delete minimal-agent
```

`delete` removes the workspace record and disk. If the VM is still running,
`delete` offers to stop it for you (confirm the prompt, or pass `--yes`);
halting first, as above, is fine too.

## Try it with another provider

The agent's shape does not depend on which model it talks to.
[`microagent init`](/cli/init/) scaffolds the same project - same protocol,
tools, workspace, and walkthrough - for OpenAI (Chat Completions with function
calling) or Gemini (function calling):

```bash
microagent init minimal-agent --provider openai   # or gemini
```

Pass the matching API-key env var (`OPENAI_API_KEY` / `GEMINI_API_KEY`) in
`create`; everything else stays the same.

## Demo limits

This example runs one request per restart and uses an env-var API key. For a
production setup, change two things:

- **One request per restart.** A real deployment streams `WorkRequest`/`WorkResult` over the mediation channel instead of `microagent cp` - see [build agents on the mediation channel](/guides/agents-and-mediation/).
- **The agent holds the key.** Passing `ANTHROPIC_API_KEY` (or `OPENAI_API_KEY`, `GEMINI_API_KEY`) as an env var means the agent reaches the model directly. The production shape routes the call through a host-side proxy that holds the key, audits requests, and forwards them. See [agency](https://github.com/geoffbelknap/agency) for an implementation.

## Related

- [`microagent.yaml`](/cli/spec/) - the full workspace spec reference.
- [Glossary](/concepts/glossary/) - workspace, mediation, halt vs quarantine, etc.
- [State and identity](/concepts/state-and-identity/) - how lifecycle events are emitted and what `microagent --json status` reports.
- [`examples/minimal-agent/`](https://github.com/geoffbelknap/microagent/tree/main/examples/minimal-agent) - the agent source.
