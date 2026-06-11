---
title: Run your first agent
description: "Run an LLM agent in a microVM: send requests, read results, resume it, or use a local model."
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

Run an [agent](/concepts/glossary/#vms-and-whats-inside-them) inside a microVM: a small program that
calls an LLM with `bash`, `read_file`, and `write_file` tools, does real work
in its own workspace, and reports a structured result. The example ships in
three flavors: Anthropic Claude, OpenAI, and Google Gemini. The flow is
identical; only the example folder and the API key env var change.

*If you just want to see microagent boot a microVM and run a command, start
with the [quickstart](/getting-started/quickstart/).*

## Before you start

1. [Install microagent](/getting-started/install/) and run `microagent doctor`.
2. Pick a provider and set the matching API key:

   | Provider | Example folder | API key env var | Sign up |
   |---|---|---|---|
   | Anthropic Claude | [`examples/minimal-agent`](https://github.com/geoffbelknap/microagent/tree/main/examples/minimal-agent) | `ANTHROPIC_API_KEY` | [console.anthropic.com](https://console.anthropic.com) |
   | OpenAI | [`examples/minimal-agent-openai`](https://github.com/geoffbelknap/microagent/tree/main/examples/minimal-agent-openai) | `OPENAI_API_KEY` | [platform.openai.com](https://platform.openai.com) |
   | Google Gemini | [`examples/minimal-agent-gemini`](https://github.com/geoffbelknap/microagent/tree/main/examples/minimal-agent-gemini) | `GEMINI_API_KEY` | [aistudio.google.com](https://aistudio.google.com) |

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
   instead of `minimal-agent`, and its `agent.py`, `protocol.py`, and the two
   walkthrough requests are identical to the example. Adjust the commands
   below to your name and run from the generated directory (use
   `--file microagent.yaml`). The [more requests to try](#more-requests-to-try)
   live in the example folder only.

The rest of this page uses the **Anthropic** example. To follow along with
OpenAI or Gemini instead, swap `minimal-agent` for `minimal-agent-openai` or
`minimal-agent-gemini` in every command, and use the matching API key env var.

## Create the workspace

```bash
microagent create \
  --file examples/minimal-agent/microagent.yaml \
  --env ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY
```

`create` takes no `--name` here: the spec's `name:` field sets the workspace
name to `minimal-agent`, and that's what the rest of the commands refer to.
First-time create takes a minute or two: microagent
pulls the base Python image, builds the rootfs, installs Pydantic and the
Anthropic SDK, and copies the agent source in. The API key is passed in as an
env var so it stays out of the spec file.

## Send a request

The agent reads requests from `/workspace/input.json`. Drop the first one in
with `microagent cp`:

```bash
microagent cp examples/minimal-agent/demo/input-001.json minimal-agent:/workspace/input.json
```

The request asks for a concrete task: install the `rich` package with pip,
write a script that renders a table of the 5 largest files under `/usr`, run
it, and include the rendered table in the summary.

## Run it

```bash
microagent start minimal-agent
```

The agent boots, calls the LLM with `bash` / `read_file` / `write_file`
tools, runs the tool calls inside `/workspace`, and writes a `WorkResult` to
`/workspace/result.json` (declared as the `result` output artifact in the
spec; `microagent --json result` surfaces it inside its `result` envelope).
`start` returns once the VM boots, not when the agent finishes - poll
`microagent --json status minimal-agent` until it reports `"state": "stopped"`
(half a minute or so), then read the result:

```bash
microagent --json result minimal-agent
```

The file looks like:

```json
{
  "request_id": "req-001",
  "status": "completed",
  "content": "Done! Here's a summary of what I accomplished:\n\n1. **Installed `rich`** via pip (version 15.0.0, along with its dependencies)...",
  "error": null,
  "completed_at": "2026-06-11T17:52:32.281678Z",
  "audit_ref": "audit://req-001"
}
```

The `content` string is the LLM's wording, so it varies run to run; the other
fields (`request_id`, `status`, `audit_ref`) echo the request. This run's
`content` ended with the table the agent rendered inside the VM:

```text
                           5 Largest Files Under /usr
┏━━━━━━┳━━━━━━━━━━┳━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓
┃ Rank ┃     Size ┃ Path                                                       ┃
┡━━━━━━╇━━━━━━━━━━╇━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┩
│    1 │ 6.22 MiB │ /usr/lib/x86_64-linux-gnu/libcrypto.so.3                   │
│    2 │ 4.99 MiB │ /usr/local/lib/libpython3.13.so.1.0                        │
│    3 │ 4.60 MiB │ /usr/sbin/microagent-init                                  │
│    4 │ 4.53 MiB │ /usr/local/lib/python3.13/site-packages/pydantic_core/_pyd │
│      │          │ antic_core.cpython-313-x86_64-linux-gnu.so                 │
│    5 │ 3.75 MiB │ /usr/bin/perl5.40.1                                        │
└──────┴──────────┴────────────────────────────────────────────────────────────┘
```

That `pip install` went into the VM's own system Python, and the scan walked
the VM's own `/usr` - this is the agent's machine to mutate, and `delete`
throws the whole thing away. (It even found its own init:
`/usr/sbin/microagent-init`, row 3.)

`microagent --json result` reads the result file and reports the run's exit
code in its `result.exitCode` field - a clean exit is `0`.

The script the LLM wrote is still on the workspace's disk. Pull it out:

```bash
microagent cp minimal-agent:/workspace/biggest.py ./biggest.py
cat ./biggest.py
```

The `/workspace/biggest.py` path is the one the request in `input-001.json`
asked for - microagent doesn't dictate it.

## Halt, ask a follow-up, resume

The workspace persists between starts - disk, files, all of it. Halt cleanly,
drop in a new request, start again. The LLM can read whatever it wrote on the
previous run.

```bash
microagent halt minimal-agent
microagent cp examples/minimal-agent/demo/input-002.json minimal-agent:/workspace/input.json
microagent start minimal-agent
microagent --json status minimal-agent   # poll until "state": "stopped"
microagent --json result minimal-agent
```

The second request asks the LLM to read `/workspace/biggest.py` from the
first run, extend it to show each file's last-modified date, and run it
again. The script is still there, so the result summarizes a diff against
work it did in a previous boot: *"Added a new `Last Modified` column (yellow,
no-wrap) to the rich `Table` ... The same 5 largest files under `/usr` were
found (same ranking and sizes as before)."*

## More requests to try

The demo folder has three more requests. Each runs the same way: halt, `cp`
the request to `/workspace/input.json`, start, read the result.

| Request | What it asks |
|---|---|
| `demo/clone-and-test.json` | Fetch [hukkin/tomli](https://github.com/hukkin/tomli) from GitHub, install it, run its pytest suite, and report the pass count. The image ships without `git` - watch the agent notice and route around it |
| `demo/analyze-file.json` | Clean a messy CSV (mixed date formats, a duplicate row, a missing value, a `999999` outlier) and write a findings report |
| `demo/hello.json` | Write and run a two-line script - the smallest possible smoke test |

`analyze-file.json` reads `/workspace/sales-sample.csv`, so copy the data in
with the request, and pull the report out after the run:

```bash
microagent halt minimal-agent
microagent cp examples/minimal-agent/demo/data/sales-sample.csv minimal-agent:/workspace/sales-sample.csv
microagent cp examples/minimal-agent/demo/analyze-file.json minimal-agent:/workspace/input.json
microagent start minimal-agent
microagent --json status minimal-agent   # poll until "state": "stopped"
microagent cp minimal-agent:/workspace/report.md ./report.md
```

The report from this run identified all four planted problems: the three
date formats, the duplicate row, the row with missing values, and the
`999999.00` outlier.

## Clean up

```bash
microagent halt minimal-agent
microagent delete minimal-agent
```

`delete` removes the workspace record and its disk.

## Run it on a local model

No API key, no cloud: [`microagent model`](/cli/model/) downloads a GGUF
model and serves it on the host with `llama-server`, and
[`create --model`](/cli/create/) pairs the workspace with it. The pairing is
part of the workspace: every [`start`](/cli/start/) re-ensures the host
server and bridges the guest to it over vsock, so the local flavor has the
same lifecycle as the cloud runs above - follow-up request included. The
OpenAI example works unchanged because pairing injects `OPENAI_BASE_URL` into
the guest and the OpenAI SDK picks it up.

Two honest caveats before you start:

- **Small models are not the hosted models above.** In our testing, Qwen2.5-Instruct at
  0.5B, 3B, and 7B (Q4_K_M) all failed this page's first request - broken
  scripts, fabricated output. The model below is the smallest we verified
  completing it end to end, and it needs a low sampling temperature to do it.
- **You need `llama-server` on the host.** Install it from
  [llama.cpp](https://github.com/ggml-org/llama.cpp) and put it on your PATH,
  or point `MICROAGENT_LLAMA_SERVER` at the binary.

Pull the model - a 2.5 GB download (`create` and `start` auto-pull a missing
blob, but pulling first makes the wait visible):

```bash
microagent model pull unsloth/Qwen3-4B-Instruct-2507-GGUF/Qwen3-4B-Instruct-2507-Q4_K_M.gguf
```

Create the workspace from the same spec file the cloud flavor uses, with a
model ref and three env vars in place of the API key:

```bash
export LLAMA_ARG_CTX_SIZE=32768   # cap the context; this model defaults to 262k, which won't fit in 16 GB RAM

microagent create \
  --file examples/minimal-agent-openai/microagent.yaml \
  --model unsloth/Qwen3-4B-Instruct-2507-GGUF/Qwen3-4B-Instruct-2507-Q4_K_M.gguf \
  -e OPENAI_API_KEY=local -e OPENAI_MODEL=qwen3-4b-instruct-2507 -e OPENAI_TEMPERATURE=0.2
```

A few notes on the flags:

- `--model` persists the pairing in the workspace record. Every `start`
  re-ensures a host `llama-server` for the ref and wires a vsock bridge into
  the guest; the agent's OpenAI client talks to it at `OPENAI_BASE_URL` with
  no in-VM network involved.
- `OPENAI_API_KEY=local` satisfies the SDK's non-empty-key requirement; the
  local server ignores it. The server also serves exactly one model, so
  `OPENAI_MODEL` is a label - any value works.
- `OPENAI_TEMPERATURE=0.2` matters. At llama-server's default sampling
  temperature this model writes buggy scripts; at 0.2 it completed the
  request reliably in our runs.
- `LLAMA_ARG_CTX_SIZE` is llama-server's own env config, inherited from
  whichever `create` or `start` launches the server - keep it exported for
  the whole walkthrough.

From here the flow is the cloud flow. Send the first request and start:

```bash
microagent cp examples/minimal-agent-openai/demo/input-001.json minimal-agent-openai:/workspace/input.json
microagent start minimal-agent-openai
```

While the agent runs, `microagent model runners` shows the pairing - the
workspace holds the host server it's talking to:

```json
{
  "runners": [
    {
      "model_ref": "hf.co/unsloth/Qwen3-4B-Instruct-2507-GGUF@main/Qwen3-4B-Instruct-2507-Q4_K_M.gguf",
      "engine": "llama.cpp",
      "host": "127.0.0.1",
      "port": 34913,
      "holders": ["minimal-agent-openai"]
    }
  ]
}
```

On an 8-core CPU host the agent phase takes a couple of minutes - pip
installs `rich`, the model loops through the same tool calls Claude made
above, and the result lands in the same place. Poll
`microagent --json status minimal-agent-openai` until it reports
`"state": "stopped"`, then pull the result out:

```bash
microagent cp minimal-agent-openai:/workspace/result.json ./result.json
```

```json
{
  "request_id": "req-001",
  "status": "completed",
  "content": "I've successfully installed the 'rich' Python package and created a script to display the 5 largest files under `/usr`...",
  "error": null,
  "completed_at": "2026-06-11T20:08:18.427820Z",
  "audit_ref": "audit://req-001"
}
```

This run's `content` carried a real rendered table - same shape as the cloud
run, and four of the five files match; the local model's script counted a
libpython symlink twice, which is about par for a 4B model:

```text
                Top 5 Largest Files in /usr
┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┳━━━━━━━━━━━━━━━┓
┃ Path                                     ┃ Size          ┃
┡━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━╇━━━━━━━━━━━━━━━┩
│ /usr/lib/x86_64-linux-gnu/libcrypto.so.3 │ 6517312 bytes │
│ /usr/local/lib/libpython3.13.so.1.0      │ 5235704 bytes │
│ /usr/local/lib/libpython3.13.so          │ 5235704 bytes │
│ /usr/sbin/microagent-init                │ 4827558 bytes │
│ /usr/local/lib/python3.13/site-packages… │ 4750616 bytes │
└──────────────────────────────────────────┴───────────────┘
```

The follow-up works exactly like the cloud one - halt, drop in the second
request, start again. `halt` releases the workspace's hold on the model
server; the next `start` re-pairs it automatically:

```bash
microagent halt minimal-agent-openai
microagent cp examples/minimal-agent-openai/demo/input-002.json minimal-agent-openai:/workspace/input.json
microagent start minimal-agent-openai
microagent --json status minimal-agent-openai   # poll until "state": "stopped"
microagent cp minimal-agent-openai:/workspace/result.json ./result-002.json
```

The `biggest.py` script from the first run is still on disk, and the local
model extends it the same way Claude did: *"I modified the `biggest.py`
script to include each file's last-modified date ... Added code to get the
last modified time of each file using `os.path.getmtime()` ... Added a new
'Last Modified' column to the table."*

Clean up when you're done - `delete` removes the workspace and releases its
model server hold:

```bash
microagent delete minimal-agent-openai
```

One release rule worth knowing: `halt`, `stop`, `kill`, and `delete` release
the hold, but an agent that exits on its own - like each run above - keeps it
until the next lifecycle verb. `microagent model stop <ref>` reclaims a
runner immediately.

## What's next

- [Build a simple agent](/guides/simple-agent/) - the same flow with more
  on the agent's structure, prompt caching, and the production-shape gaps
  (mediation channel, host-side proxy for keys).
- [`microagent.yaml`](/cli/spec/) - the full workspace spec reference.
- [State and identity](/concepts/state-and-identity/) - what `microagent --json status` reports and how lifecycle events are emitted.
- [Glossary](/concepts/glossary/) - workspace, mediation, halt vs stop vs kill vs quarantine.
