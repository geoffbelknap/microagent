---
title: Build a simple agent
description: Boot a microVM, point it at Claude, watch it write and run files in its own workspace.
---

This recipe builds an agent: a Linux microVM running a body that calls Claude with `bash`, `read_file`, and `write_file` tools. Claude can edit code, run commands, and inspect files — all inside the microVM's sandbox at `/workspace`. Halt the workspace, swap in a new prompt, start it back up, and Claude can read whatever it wrote on the previous run.

The workspace is fully described by [`examples/minimal-body/microagent.yaml`](https://github.com/geoffbelknap/microagent-kit/tree/main/examples/minimal-body/microagent.yaml). One spec file, one `microagent create` call — no Docker, no separate build step.

## What you'll need

- microagent-kit installed and `microagent doctor` passing — see [install](/getting-started/install/).
- On Linux, `pasta` for the default unprivileged network mode. Homebrew installs it as a microagent-kit dependency; on apt-based distros it's `sudo apt install passt`.
- An Anthropic API key in `ANTHROPIC_API_KEY`. Sign up at [console.anthropic.com](https://console.anthropic.com) if you don't have one.

## Step 1 — create the workspace

From the repo root:

```bash
microagent create \
  --file examples/minimal-body/microagent.yaml \
  --env ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY
```

The spec sets the workspace name to `minimal-body` — that's what the rest of these commands refer to. First-time create takes a minute or so: microagent pulls the OCI base image, builds the rootfs, and runs the `setup:` commands (which `pip install` Pydantic and the Anthropic SDK).

The spec file does the heavy lifting: pulls a stock `python:3.13-slim` image, installs `pydantic` and `anthropic` via `setup`, copies the body source and operator files into the rootfs via `files:`, sets the entrypoint, and declares the result artifact. The CLI just adds the API key as an env var (host secrets stay out of the spec).

The body's `process()` function runs an agentic loop: send the request to Claude with the three tools, execute any tool calls inside `/workspace`, feed the results back, loop until Claude returns a final answer. Prompt caching is on by default — the system prompt is stable across requests, so the body pays for it once and reads it back at ~10× cheaper afterward.

The full body source is in [`examples/minimal-body/body.py`](https://github.com/geoffbelknap/microagent-kit/tree/main/examples/minimal-body/body.py).

## Step 2 — deliver the request

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

(Full file: [`examples/minimal-body/demo/input-001.json`](https://github.com/geoffbelknap/microagent-kit/tree/main/examples/minimal-body/demo/input-001.json).)

The system prompt — already baked into the workspace by the spec — makes the agent take initiative:

```text
You are an agent running inside a Linux microVM. You have access to a workspace
at /workspace where you can run shell commands, read files, and write files
using your tools.

Help the user with their request — actually do the work, don't just describe
it. When you're finished, briefly summarize what you accomplished and where
the user can find the results.
```

## Step 3 — run and look at what happened

```bash
microagent start minimal-body
microagent --json result minimal-body
```

The body takes ~5–10 seconds to complete: VM boots, body emits `ready`, runs the structural checks, calls Claude, writes the result, exits. You'll see Claude's final summary in the `content` field — something like *"I created `/workspace/hello.py`, ran it with `python3`, and got `hello from a microVM` followed by the kernel version `6.1.155`."*

The file Claude wrote is still on the workspace's disk. Pull it out:

```bash
microagent cp minimal-body:/workspace/hello.py ./hello.py
cat ./hello.py
```

That's the script Claude wrote, retrieved from the microVM. Claude did real work in a real workspace.

## Step 4 — halt, ask a follow-up, resume

The workspace persists between starts — disk, files, all of it. Halt cleanly, deliver a new request, start it back up. Claude can read whatever it wrote on the previous run.

```bash
microagent halt minimal-body
microagent cp examples/minimal-body/demo/input-002.json minimal-body:/workspace/input.json
microagent start minimal-body
microagent --json result minimal-body
```

The second request asks Claude to read `/workspace/hello.py` and explain it. The file is still there from the first run; the system prompt is still loaded; the installed deps are still installed. Anthropic's prompt cache is still warm too — the second request reads the system prompt back at ~10× cheaper than the first paid for it.

(See [glossary](/concepts/glossary/) for halt vs stop vs kill vs quarantine.)

## Step 5 — clean up

```bash
microagent halt minimal-body
microagent delete minimal-body
```

`delete` removes the workspace record and disk. (For Firecracker, `delete` refuses while the VM is still running; halt or stop first.)

## Try it with another provider

The body's shape doesn't depend on which model it talks to. Sibling examples ship the same flow against OpenAI and Gemini — same protocol, same tools, same workspace, same recipe. Each variant has its own `microagent.yaml` and README:

- [`examples/minimal-body-openai/`](https://github.com/geoffbelknap/microagent-kit/tree/main/examples/minimal-body-openai) — OpenAI Chat Completions with function calling.
- [`examples/minimal-body-gemini/`](https://github.com/geoffbelknap/microagent-kit/tree/main/examples/minimal-body-gemini) — Google Gemini with function calling.

Swap the spec path and the API-key env var; everything else stays the same.

## What this isn't yet

This recipe runs the agent against one request per restart and uses an env-var API key. Two production-shape gaps:

- **Mediation-channel transport.** The agent receives one request at a time via `microagent cp`. A real deployment carries `WorkRequest` and `WorkResult` over the mediation channel — a guest-to-host vsock contract — so the body sees a stream of requests without restarting between them.
- **Mediation-routed egress.** Passing `ANTHROPIC_API_KEY` (or `OPENAI_API_KEY`, `GEMINI_API_KEY`) as an env var means the body holds the key and reaches the model directly. The production shape routes the call through a host-side proxy that holds the key, audits requests, and forwards them. See [agency](https://github.com/geoffbelknap/agency) for an implementation.

## What to read next

- [`microagent.yaml`](/cli/spec/) — the full workspace spec reference.
- [Glossary](/concepts/glossary/) — workspace, mediation, halt vs quarantine, etc.
- [State and identity](/concepts/state-and-identity/) — how lifecycle events are emitted and what `microagent --json status` reports.
- [`examples/minimal-body/`](https://github.com/geoffbelknap/microagent-kit/tree/main/examples/minimal-body) — the body source.
