---
title: microagent serve
description: Serve machine-readable agent endpoints.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-08_

```text
microagent serve mcp
```

`serve` starts long-running machine-readable endpoints for agent clients. The
current endpoint is `mcp`, which serves the MicroAgent MCP stdio transport from
the main `microagent` binary.

The MCP server automatically uses AX output mode. It exposes structured tools
for workspace lifecycle, inspection, results, stats, logs, events, snapshots,
images, networks, volumes, copy/artifact access, host diagnostics, capability
discovery, and cost estimation.

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
| `profiles.list` | List resource profiles |
| `host.inspect` | Report host capabilities |
| `doctor.check` | Run host diagnostics |
| `host.networking.setup` | Apply or revert Linux privileged networking after preview confirmation |
| `contract.get` | Return the backend-neutral runtime contract |
| `kernel.verify` | Verify a kernel artifact |
| `kernel.install` | Install a kernel artifact after preview confirmation |
| `rootfs.build` | Build a rootfs after preview confirmation |
| `cp` | Copy files into or out of stopped workspace disks |

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

## Example

```bash
microagent serve mcp
```

## Related

- [`contract`](/cli/contract/) - the backend-neutral runtime contract the MCP surface exposes
- [Supervisor protocol](/protocol/) - the JSON shapes returned by the underlying commands
