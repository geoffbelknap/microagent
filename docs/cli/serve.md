---
title: microagent serve
description: Run the MCP stdio server for agent clients.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-30_

```text
microagent serve mcp [--state-dir <dir>] [--supervisor <path>]   Stdio MCP transport for agent clients
```

`microagent serve mcp` is the MCP client integration entry point. A client
launches it as a foreground stdio subprocess; it is not a normal interactive
CLI command and is not advertised in top-level help. When started directly from
a terminal, the command exits with setup guidance instead of waiting for MCP
frames on stdin.

The MCP server is microagent's agent-facing surface. Its agent experience (AX)
adds compact defaults, bounded polling, structured actionable errors,
idempotency, confirmation previews, and next-decision guidance to the MCP
protocol. It exposes typed tools for workspace lifecycle, one-shot task
dispatch, inspection, results, stats, logs, events, egress audit, snapshots,
images, networks, volumes, model store/serving, copy/artifact access, host
diagnostics, capability discovery, and cost estimation. It does not route
agent calls through the CLI's presentation mode.

`snapshot.create` accepts `forensic: true`, which captures for investigation
rather than restore: guest secrets are retained (credential material is the
evidence) and the capture is not restorable. The artifact is secret-bearing
from that point, so route it to storage the workloads it came from cannot read.

The MCP server stops at VM operations: it does not plan, call an LLM, or
interpret audit meaning. Tools such as `workspace.create` and
`workspace.dispatch` can configure credential brokering and egress policy for
the mediator, but the MCP server itself never holds secrets and never makes
policy decisions.

## Examples

Probe the MCP transport without an MCP client:

```bash
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}' | microagent serve mcp
```

```text
{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"tools":{}},"protocolVersion":"2025-06-18","serverInfo":{"name":"microagent","version":"<version>"}}}
```

In normal use you never run `serve mcp` yourself - your MCP client launches it.
The transport accepts both `Content-Length`-framed MCP messages and raw JSON
lines on stdin, and replies in whichever framing the client used first.

## Commands

| Command | Purpose |
|---|---|
| `mcp` | MCP client-launched stdio integration entry point |

`serve` has no other subcommands. To serve a local GGUF model on the host, use
[`microagent model serve`](/cli/model/) instead.

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
one. The server uses `~/.microagent/` by default. To expose another state root,
configure it when the MCP client launches the server:

```text
command: microagent
args: ["serve", "mcp", "--state-dir", "/path/to/state"]
```

The state root and optional supervisor executable are fixed for the lifetime of
the server process. They are not MCP tool arguments, and per-call attempts to
set `state_dir` or `supervisor` are rejected. Configure separate MCP server
entries when an operator needs to expose more than one state root. Append the
launch flags to the CLI command or JSON client's `args` array.

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

The tools fall into five families. Call `microagent.describe` at runtime for
the full machine-readable input schema of every tool.

### Workspace lifecycle

| Tool | Purpose |
|---|---|
| `workspace.create` | Create or dry-run a workspace, including snapshot forks with `from_snapshot` |
| `workspace.start` | Start a prepared workspace, including snapshot restore with `from_snapshot` |
| `workspace.wait` | Block until a workspace reaches a terminal state, replacing `workspace.inspect` polling loops |
| `workspace.exec` | Run a structured command in a running workspace |
| `workspace.dispatch` | Run one task in a fresh, isolated, single-use workspace under egress guardrails, tear it down, and return the result plus a summary of what the workspace reached on the network |
| `workspace.halt` | Halt a workspace and preserve disk state |
| `workspace.kill` | Force stop a workspace runtime |
| `workspace.pause` | Pause a running workspace when supported |
| `workspace.resume` | Resume a paused workspace when supported |
| `workspace.quarantine` | Sever host-side network and mediation |
| `workspace.delete` | Delete a workspace, with optional preview and force |
| `workspace.clone` | Clone a stopped workspace |
| `workspace.apply` | Apply supported changes from a workspace spec file |
| `workspace.commit` | Commit a stopped workspace rootfs; targets default to `local/...` or loopback registries, with `allow_registry_shadow` for registry identity |
| `workspace.estimate_cost` | Estimate workspace resources before action |

### Observe and inspect

| Tool | Purpose |
|---|---|
| `workspace.list` | List saved workspaces |
| `workspace.inspect` | Inspect workspace state with `summary` or `full` output |
| `workspace.result` | Read the structured workspace result |
| `workspace.stats` | Sample workspace resource usage |
| `workspace.logs` | Read workspace serial logs with `summary` or `full` output |
| `workspace.events` | Read lifecycle events with `summary` or `full` output |
| `workspace.egress` | Read the egress mediator's audit decisions (allow/deny/MITM/DNS/UDP) for a workspace |
| `network.inspect` | Inspect a workspace's network |

### Files, artifacts, and snapshots

| Tool | Purpose |
|---|---|
| `cp` | Copy files into or out of stopped workspace disks |
| `artifacts.list` | List declared workspace artifacts |
| `artifacts.get` | Retrieve a declared workspace artifact |
| `snapshot.create` | Create a backend snapshot when supported |
| `snapshot.list` | List workspace snapshots |
| `snapshot.delete` | Delete a workspace snapshot, with optional preview |

### Images, volumes, and models

| Tool | Purpose |
|---|---|
| `images.pull` | Pull a reusable image rootfs |
| `images.list` | List reusable local image records |
| `images.push` | Push a locally committed OCI image |
| `images.tag` | Tag a local image record |
| `images.delete` | Delete a local image record, with optional preview |
| `images.prune` | Prune stale local image records, with optional preview |
| `volume.create` | Create a named managed ext4 volume |
| `volume.list` | List named managed volumes |
| `volume.inspect` | Inspect a named managed volume |
| `volume.delete` | Delete a named managed volume, with optional preview and force |
| `models.pull` | Pull a GGUF model from HuggingFace into the local store |
| `models.list` | List locally stored models |
| `models.remove` | Remove a model from the local store |
| `models.prune` | Prune local model records whose blobs are missing |
| `models.serve` | Start or reuse a local host model server for a stored or pulled model |
| `models.stop` | Stop local host model server instances for a model |
| `models.runners` | List running local model servers |
| `models.policy.validate` | Validate a structured model mediation policy file |
| `models.policy.evaluate` | Dry-run a policy file against structured request metadata |

### Host and safety

| Tool | Purpose |
|---|---|
| `microagent.describe` | Return the machine-readable capability manifest |
| `microagent.ping` | Validate the MCP transport |
| `profiles.list` | List resource profiles |
| `host.inspect` | Report host capabilities |
| `doctor.check` | Run host diagnostics |
| `contract.get` | Return the runtime fields integrations rely on |
| `kernel.verify` | Verify a kernel artifact |
| `kernel.install` | Install a kernel artifact after preview confirmation |
| `rootfs.build` | Build a rootfs after preview confirmation |

The `models.*` tools mirror the [`model`](/cli/model/) subcommands - the same
local store and host runner management over MCP.

### Common arguments

The state root and supervisor executable are server launch configuration, not
tool arguments. Most tools share a small set of optional arguments:

- `preview` - on destructive tools, return the actions that would be taken
  without changing host state.
- `idempotency_key` - on mutation tools, a client-supplied key. For 15 minutes,
  retries by the same principal with identical arguments replay the first
  completed envelope instead of re-running. Reusing a key with different
  arguments returns a structured `conflict`.
- `principal` - an optional caller-identity object (`workload_identity`,
  `delegated_authority`, `purpose`, `correlation_id`) echoed back as
  `principal_context` for audit trails.

`microagent.describe` returns the full per-tool schemas, including which tools
accept which of these.

`connect`, streaming `logs`/`events`/`stats`, `supervise`, `perf`, `init`, and
`secret check` remain CLI-only. They are interactive, streaming, benchmarking,
project scaffolding, or secret-boundary workflows that need more specific MCP
interaction and permission semantics than a bounded request/response tool.

## Output

Every MCP tool response is the same envelope: `{ok, result, meta}` on success,
a JSON-RPC error with a matching `error.data` shape on failure. `result` holds
the tool's answer; `meta` carries transport facts (`timing_ms`,
`principal_context`, and, for mutation tools, `idempotency_replay`) as a
sibling, never mixed into `result`.

A trimmed success response - the JSON object inside `result.content[].text`:

```json
{
  "ok": true,
  "result": { "workspace": "research", "state": "running" },
  "meta": { "timing_ms": 42, "principal_context": null }
}
```

A failure stays a JSON-RPC error (never a tool payload); `error.data` holds
the plain `structuredError` shape plus the same sibling `meta` block:

```json
{
  "jsonrpc": "2.0",
  "id": 7,
  "error": {
    "code": -32000,
    "message": "workspace not found",
    "data": {
      "kind": "not_found",
      "message": "workspace \"research\" not found",
      "remediation": "Run workspace.list to inspect available workspaces, or workspace.create to create the requested workspace.",
      "retryable": false,
      "correlation_id": "req-8f3c2e",
      "meta": { "timing_ms": 12, "principal_context": null }
    }
  }
}
```

Read the correlation id from `error.data.correlation_id`, but don't hardcode
that path - call `microagent.describe` and read each operation's
`correlation_id_key` instead. It's the versioned contract for where the
correlation id lives in that response, so a future transport change can move
it without breaking callers that follow the manifest.

`workspace.wait` blocks until the workspace reaches a terminal state
(`stopped`, `halted`, `failed`, `quarantined`, or `prepared`) and returns
`{workspace, state, ok}`, where `ok` is `true` for a clean finish (`stopped`,
`halted`, `prepared`). Use it after `workspace.start` instead of polling
`workspace.inspect`; pass `timeout` (a Go duration such as `"5m"`) to bound
the call - an elapsed timeout returns a retryable `transient` error.

`workspace.create`, `workspace.inspect`, `workspace.logs`, and
`workspace.events` default to compact `summary` output so repeated agent state
checks do not require full event history or full serial logs.
`workspace.create` summaries report the outcome, state, a `ready` flag, and
`next_decision_points` listing the tools that make sense to call next. For the
inspect, logs, and events tools, pass `format: "full"` when the complete typed
result is required. `workspace.logs` accepts `tail_lines` for bounded log
polling. `workspace.events` accepts `limit` and `after_index`, and returns
`next_after_index`; pass that value as the next `after_index` to poll for
new events without a long-running `events --follow` call.

`workspace.delete`, `volume.delete`, `snapshot.delete`,
`images.delete`, and `images.prune` accept `preview: true` to return the
actions that would be taken without changing host state. Mutating tools accept
an optional `idempotency_key`. The cache is scoped by tool and principal,
coalesces concurrent identical calls, retains at most 1,024 entries for 15
minutes, and rejects same-key/different-argument reuse. A changed
`correlation_id` does not prevent a legitimate retry from replaying.

Snapshot restore and fork use the same workspace tools as the CLI. Pass
`from_snapshot: "<tag>"` to `workspace.start` to restore a workspace in place,
or `from_snapshot: "<workspace>:<tag>"` to `workspace.create` to fork a new
workspace from an existing snapshot. The dedicated `snapshot.*` tools create,
list, and delete snapshot records.

`kernel.install` and `rootfs.build` use a stricter
preview-confirm contract. Call the tool with `preview: true` first, inspect the
returned `actions`, then call the same tool with `confirm_token` set to the
returned `confirmation_token`. Calls without the matching token fail before
changing host state.

`workspace.exec` returns the structured exec result directly under `result`:
`status`, optional `exit_code`, base64-encoded `stdout` and `stderr`,
truncation flags, timestamps, protocol version, and optional service error. A
nonzero command exit is not a tool error; it is represented by `status:
exited` and a nonzero `exit_code`. Successful `workspace.exec` responses carry
`retry_count` and `retry_wall_clock_ms` under `meta`, alongside the usual
`timing_ms` and `principal_context`. When the bounded retry budget is
exhausted, `error.data.meta` includes `retry_count`, `retry_wall_clock_ms`, and
`retry_exhausted` so clients can distinguish retry exhaustion from ordinary
task failure. These retry semantics come from the shared typed workspace exec
operation rather than CLI presentation behavior.

## Flags

- `--state-dir <dir>` - state root exposed through this MCP server. Defaults to
  `~/.microagent/`.
- `--supervisor <path>` - supervisor executable used by this MCP server.

These values are operator-owned process configuration. The MCP tool schemas do
not expose them, and tool calls cannot override them. Run another configured
server process when a client needs access to a different state root or
supervisor.

See [global flags](/cli/#global-flags) for `--output`/`--json`.

## Exit status

`serve mcp` runs until its client closes stdin, then exits `0`; started from a
terminal, it exits nonzero with setup guidance.

## Related

- [Use the MCP server](/guides/mcp-server/) - the client-setup walkthrough
- [`model`](/cli/model/) - model store and runner management
- [`contract`](/cli/contract/) - the runtime fields integrations rely on
- [State and identity](/concepts/state-and-identity/) - lifecycle states and readiness fields
