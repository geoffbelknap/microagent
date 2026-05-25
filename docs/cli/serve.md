---
title: microagent serve
description: Serve machine-readable agent endpoints.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-25_

```text
microagent serve mcp
```

`serve` starts long-running machine-readable endpoints for agent clients. The
initial endpoint is `mcp`, which serves the MicroAgent MCP stdio transport from
the main `microagent` binary.

The MCP server automatically uses AX output mode. It exposes structured tools
for workspace lifecycle, inspection, images, copy/artifact access, capability
discovery, and cost estimation.

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
| `workspace.delete` | Delete a workspace, with optional preview |
| `workspace.list` | List workspaces |
| `workspace.inspect` | Inspect workspace state with `summary` or `full` output |
| `workspace.estimate_cost` | Estimate workspace resources before action |
| `images.pull` | Pull a reusable image rootfs |
| `images.list` | List reusable local image records |
| `cp` | Copy files into or out of stopped workspace disks |
| `artifacts.get` | Retrieve a declared workspace artifact |

## Output

MCP tool responses are structured for agent clients. Mutation tools return a
consistent envelope with `result`, optional structured `error`, `timing_ms`, and
`principal_context` fields.

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
