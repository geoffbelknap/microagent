---
title: microagent serve
description: Serve machine-readable agent endpoints.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-14_

```text
microagent serve mcp                                                              Stdio MCP transport for agent clients
```

`microagent serve mcp` exists as an MCP client integration entry point. It is a
foreground stdio transport that a client launches as a subprocess; it is not a
normal interactive CLI command and is intentionally not advertised in top-level
help. When started directly from a terminal, the command exits with setup
guidance instead of waiting for protocol frames on stdin.

Serve local GGUF models with [`microagent model serve`](/cli/model/).

The MCP server automatically uses AX output mode. It exposes structured tools
for workspace lifecycle, inspection, results, stats, logs, events, snapshots,
images, networks, volumes, model store/serving, copy/artifact access, host
diagnostics, capability discovery, and cost estimation.

It is the full microagent MCP surface for the current release. It intentionally
stops at substrate operations: it does not plan, call an LLM, interpret audit
meaning, broker credentials, or make policy decisions.

## Examples

Serve a model:

```bash
microagent model serve TheBloke/Llama-2-7B-GGUF/llama-2-7b.Q4_K_M.gguf
```

Probe the MCP transport without an MCP client:

```bash
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}' | microagent serve mcp
```

```text
{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"tools":{}},"protocolVersion":"2025-06-18","serverInfo":{"name":"microagent","version":"0.8.0"}}}
```

In normal use you never run `serve mcp` yourself - your MCP client launches it.

## Commands

| Command | Purpose |
|---|---|
| `mcp` | MCP client-launched stdio integration entry point |
| `model` | Serve a local HuggingFace GGUF model |

## Configure MCP clients

Install `microagent` on the same host where your coding tool will launch the MCP
server, then verify the host backend there:

```bash
microagent doctor
```

For every stdio MCP client, add microagent as a local stdio MCP server:

```text
command: microagent
args: ["serve", "mcp"]
```

That snippet belongs in your MCP client's server configuration. If the client
is a GUI app or a remote editor session that does not inherit your shell
`PATH`, use the absolute path from `command -v microagent` as the `command`
value. Do not configure `microagent serve mcp` as an HTTP/SSE server or
background daemon; the MCP client must start it as a foreground stdio process.

For long-running operations such as image pulls, rootfs builds, and VM
lifecycle calls, raise the client's MCP tool timeout when the client supports
one. The microagent tools use `~/.microagent/` by default; most tools also accept a
`state_dir` argument when a caller needs an explicit state root.

The examples below intentionally show the client configuration instead of a
microagent installer command. MCP clients store settings in different files,
support different timeout fields, and may run locally, remotely, or inside an
editor profile. The reliable installation contract is the stdio command above.

### Codex

```bash
codex mcp add microagent -- microagent serve mcp
```

Or edit `~/.codex/config.toml` or a trusted project `.codex/config.toml`:

```toml
[mcp_servers.microagent]
command = "microagent"
args = ["serve", "mcp"]
startup_timeout_sec = 20
tool_timeout_sec = 600
```

### Claude Code

```bash
claude mcp add --transport stdio --scope user microagent -- microagent serve mcp
```

For a project-shared Claude Code configuration, put this in `.mcp.json` at the
project root:

```json
{
  "mcpServers": {
    "microagent": {
      "command": "microagent",
      "args": ["serve", "mcp"],
      "timeout": 600000
    }
  }
}
```

For a project-shared server, use `--scope project` instead of `--scope user`.

### VS Code

For a workspace configuration, create `.vscode/mcp.json`:

```json
{
  "servers": {
    "microagent": {
      "type": "stdio",
      "command": "microagent",
      "args": ["serve", "mcp"]
    }
  }
}
```

You can also add the user-profile server from a shell where `microagent` is on
`PATH`:

```bash
code --add-mcp '{"name":"microagent","command":"microagent","args":["serve","mcp"]}'
```

If VS Code is connected to a remote machine and you want microagent to run
there, define the server in the remote workspace or remote user MCP
configuration.

### GitHub Copilot CLI

Add microagent to `~/.copilot/mcp-config.json`:

```json
{
  "mcpServers": {
    "microagent": {
      "type": "local",
      "command": "microagent",
      "args": ["serve", "mcp"],
      "env": {},
      "tools": ["*"]
    }
  }
}
```

If your Copilot CLI session does not inherit the same `PATH` as your shell, use
the absolute path from `command -v microagent` as the `command` value.

### Other MCP clients

Use the client's local stdio server form. If it asks for a single command and
arguments, enter `microagent` and `serve`, `mcp`. If it uses Claude-style JSON,
the minimum shape is:

```json
{
  "mcpServers": {
    "microagent": {
      "command": "microagent",
      "args": ["serve", "mcp"]
    }
  }
}
```

## MCP tools

| Tool | Purpose |
|---|---|
| `microagent.describe` | Return the machine-readable capability manifest |
| `microagent.ping` | Validate the MCP transport |
| `workspace.create` | Create or dry-run a workspace |
| `workspace.start` | Start a prepared workspace |
| `workspace.exec` | Run a structured command in a running workspace |
| `workspace.halt` | Halt a workspace and preserve disk state |
| `workspace.stop` | Stop a workspace runtime |
| `workspace.kill` | Force stop a workspace runtime |
| `workspace.quarantine` | Sever host-side network and mediation |
| `workspace.pause` | Pause a running workspace when supported |
| `workspace.resume` | Resume a paused workspace when supported |
| `workspace.delete` | Delete a workspace, with optional preview and force |
| `workspace.list` | List workspaces |
| `workspace.inspect` | Inspect workspace state with `summary` or `full` output |
| `workspace.result` | Read the structured workspace result |
| `workspace.stats` | Sample workspace resource usage |
| `workspace.logs` | Read workspace serial logs with `summary` or `full` output |
| `workspace.events` | Read lifecycle events with `summary` or `full` output |
| `workspace.clone` | Clone a stopped workspace |
| `workspace.apply` | Apply supported changes from a workspace spec file |
| `workspace.commit` | Commit a stopped workspace rootfs to an OCI image |
| `workspace.estimate_cost` | Estimate workspace resources before action |
| `artifacts.list` | List declared workspace artifacts |
| `artifacts.get` | Retrieve a declared workspace artifact |
| `snapshot.create` | Create a backend snapshot when supported |
| `snapshot.list` | List workspace snapshots |
| `snapshot.delete` | Delete a workspace snapshot, with optional preview |
| `network.inspect` | Inspect a named microVM network |
| `network.create` | Create a named microVM network record |
| `network.list` | List named microVM network records |
| `network.delete` | Delete a named microVM network record, with optional preview and force |
| `volume.create` | Create a named managed ext4 volume |
| `volume.list` | List named managed volumes |
| `volume.inspect` | Inspect a named managed volume |
| `volume.delete` | Delete a named managed volume, with optional preview and force |
| `images.pull` | Pull a reusable image rootfs |
| `images.list` | List reusable local image records |
| `images.push` | Push a locally committed OCI image |
| `images.tag` | Tag a local image record |
| `images.delete` | Delete a local image record, with optional preview |
| `images.prune` | Prune stale local image records, with optional preview |
| `models.pull` | Pull a GGUF model from HuggingFace into the local store |
| `models.list` | List locally stored models |
| `models.remove` | Remove a model from the local store |
| `models.prune` | Prune local model records whose blobs are missing |
| `models.serve` | Start or reuse a local host model server for a stored or pulled model |
| `models.stop` | Stop local host model server instances for a model |
| `models.runners` | List running local model servers |
| `profiles.list` | List resource profiles |
| `host.inspect` | Report host capabilities |
| `doctor.check` | Run host diagnostics |
| `host.networking.setup` | Apply or revert Linux privileged networking after preview confirmation |
| `contract.get` | Return the backend-neutral runtime contract |
| `kernel.verify` | Verify a kernel artifact |
| `kernel.install` | Install a kernel artifact after preview confirmation |
| `rootfs.build` | Build a rootfs after preview confirmation |
| `cp` | Copy files into or out of stopped workspace disks |

The `models.*` tools mirror the [`model`](/cli/model/) subcommands - the same
local store and host runner management over MCP.

`connect`, streaming `logs`/`events`/`stats`, `supervise`, `perf`, `init`, and
`secret check` remain CLI-only. They are interactive, streaming, benchmarking,
project scaffolding, or secret-boundary workflows that need more specific MCP
interaction and permission semantics than a bounded request/response tool.

## Output

MCP tool responses are structured for agent clients. Mutation tools return a
consistent envelope with `result`, optional structured `error`, `timing_ms`, and
`principal_context` fields.

`workspace.inspect`, `workspace.logs`, and `workspace.events` default to compact
`summary` output so repeated agent state checks do not require full event
history or full serial logs. Pass `format: "full"` when the complete underlying
AX payload is required. `workspace.logs` accepts `tail_lines` for bounded log
polling. `workspace.events` accepts `limit` and `after_index`, and returns
`next_after_index`; pass that value as the next `after_index` to poll for
new events without a long-running `events --follow` call.

`workspace.delete`, `network.delete`, `volume.delete`, `snapshot.delete`,
`images.delete`, and `images.prune` accept `preview: true` to return the
actions that would be taken without changing host state. Mutating tools accept
an optional `idempotency_key`; tools that are not inherently idempotent replay
the first successful MCP envelope for a client-supplied key.

`host.networking.setup`, `kernel.install`, and `rootfs.build` use a stricter
preview-confirm contract. Call the tool with `preview: true` first, inspect the
returned `actions`, then call the same tool with `confirm_token` set to the
returned `confirmation_token`. Calls without the matching token fail before
changing host state.

`workspace.exec` returns the structured exec result directly under `result`:
`status`, optional `exit_code`, base64-encoded `stdout` and `stderr`,
truncation flags, timestamps, protocol version, and optional service error. A
nonzero command exit is not a tool error; it is represented by `status:
exited` and a nonzero `exit_code`. Successful `workspace.exec` responses also
include `retry_count`, `retry_wall_clock_ms`, and matching `metadata` fields.
When the bounded retry budget is exhausted, the JSON-RPC error `data` includes
`retry_count`, `retry_wall_clock_ms`, and `retry_exhausted` so clients can
distinguish retry exhaustion from ordinary task failure. These retry semantics
come from the shared workspace exec layer and match CLI AX exec behavior.

## Flags

`serve mcp` takes no flags. `model serve` takes the same flags as
[`model serve`](/cli/model/):

| Flag | Description |
|---|---|
| `--dedicated` | Start a dedicated runner for this caller instead of reusing a shared one |
| `--token <t>` | HuggingFace bearer token used if the model must be auto-pulled |
| `--state-dir <dir>` | State directory (default `~/.microagent/`) |

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Exit status

`model serve` exits `0` when the runner is started or reused; nonzero when no
`llama-server` binary is found or the model cannot be pulled. `serve mcp` runs
until its client closes stdin, then exits `0`; started from a terminal, it
exits nonzero with setup guidance. In AX mode a failure is written as a
structured error envelope.

## Related

- [Use the MCP server](/guides/mcp-server/) - the client-setup walkthrough
- [`model`](/cli/model/) - model store and runner management
- [`contract`](/cli/contract/) - the backend-neutral runtime contract the MCP surface exposes
- [Supervisor protocol](/protocol/) - the JSON shapes returned by the underlying commands
