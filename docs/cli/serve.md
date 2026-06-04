---
title: microagent serve
description: Serve machine-readable agent endpoints.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-03_

```text
microagent serve mcp
```

`serve` starts long-running machine-readable endpoints for agent clients. The
current endpoint is `mcp`, which serves the MicroAgent MCP stdio transport from
the main `microagent` binary.

The MCP server automatically uses AX output mode. It exposes structured tools
for workspace lifecycle, inspection, logs, events, images, networks, volumes,
copy/artifact access, capability discovery, and cost estimation.

It is the full microagent MCP surface for the current release. It intentionally
stops at substrate operations: it does not plan, call an LLM, interpret audit
meaning, broker credentials, or make policy decisions.

## Commands

| Command | Purpose |
|---|---|
| `mcp` | Serve the MicroAgent MCP stdio endpoint |

## MCP tools

| Tool | Purpose |
|---|---|
| `microagent.describe` | Return the machine-readable capability manifest |
| `microagent.ping` | Validate the MCP transport |
| `workspace.create` | Create or dry-run a workspace |
| `workspace.start` | Start a prepared workspace |
| `workspace.exec` | Run a structured command in a running workspace |
| `workspace.halt` | Halt a workspace and preserve disk state |
| `workspace.delete` | Delete a workspace, with optional preview and force |
| `workspace.list` | List workspaces |
| `workspace.inspect` | Inspect workspace state with `summary` or `full` output |
| `workspace.logs` | Read workspace serial logs with `summary` or `full` output |
| `workspace.events` | Read lifecycle events with `summary` or `full` output |
| `workspace.commit` | Commit a stopped workspace rootfs to an OCI image |
| `workspace.estimate_cost` | Estimate workspace resources before action |
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
| `cp` | Copy files into or out of stopped workspace disks |
| `artifacts.get` | Retrieve a declared workspace artifact |

## Output

MCP tool responses are structured for agent clients. Mutation tools return a
consistent envelope with `result`, optional structured `error`, `timing_ms`, and
`principal_context` fields.

`workspace.inspect`, `workspace.logs`, and `workspace.events` default to compact
`summary` output so repeated agent state checks do not require full event
history or full serial logs. Pass `format: "full"` when the complete underlying
AX payload is required. `workspace.events` also accepts `limit` for summary
event count.

`workspace.delete`, `network.delete`, and `volume.delete` accept
`preview: true` to return the actions that would be taken without changing host
state. Mutating tools accept an optional `idempotency_key`; tools that are not
inherently idempotent replay the first successful MCP envelope for a
client-supplied key.

`workspace.exec` returns the structured exec result directly under `result`:
`status`, optional `exit_code`, base64-encoded `stdout` and `stderr`,
truncation flags, timestamps, protocol version, and optional service error. A
nonzero command exit is not a tool error; it is represented by `status:
exited` and a nonzero `exit_code`. Successful `workspace.exec` responses also
include `retry_count`, `retry_wall_clock_ms`, and matching `metadata` fields.
When the bounded retry budget is exhausted, the JSON-RPC error `data` includes
`retry_count`, `retry_wall_clock_ms`, and `retry_exhausted` so clients can
distinguish retry exhaustion from ordinary task failure.

## Example

```bash
microagent serve mcp
```

## Related

- [`contract`](/cli/contract/) - the backend-neutral runtime contract the MCP surface exposes
- [Supervisor protocol](/protocol/) - the JSON shapes returned by the underlying commands
