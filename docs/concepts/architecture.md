---
title: How microagent fits
description: Pick the CLI, MCP endpoint, or Go library entry point.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-27_

There are three useful entry points:

- Use the **CLI** when a human or shell script is running workspaces.
- Use **MCP** when a coding tool or agent client needs structured workspace
  tools over stdio.
- Use the **Go library** when workspace lifecycle is part of your program and
  you want typed options, typed results, and direct error handling.

All three call the same Go packages. The CLI and MCP server are adapters, not
separate runtimes.

```text
shell, MCP client, or Go program
  └─ microagent packages
       ├─ pkg/workspace ─ workspace lifecycle and exec
       ├─ pkg/rootfs · pkg/kernel · pkg/imagecache · pkg/diagnostics
       └─ pkg/vmkit ─ supervisor dispatch
            └─ host supervisor

OCI image ──► pkg/rootfs ──► ext4 disk ──► microVM
```

## Which entry point to use

- Start with [`microagent run`](/cli/run/) for one-shot work.
- Use [`microagent create`](/cli/create/), [`start`](/cli/start/),
  [`exec`](/cli/exec/), and [`delete`](/cli/delete/) for named workspaces.
- Use [`microagent serve mcp`](/cli/serve/) for MCP clients.
- Use the [Go library](/library/) when you are embedding microagent in a
  runtime, scheduler, local developer tool, or backend service.

## What the packages do

`pkg/workspace` owns workspace lifecycle, state, disks, identity, exec,
results, and artifacts. `pkg/rootfs` turns OCI images into bootable ext4 disks.
`pkg/kernel` installs and verifies default kernels. `pkg/imagecache` manages
reusable local rootfs baselines. `pkg/diagnostics` powers `microagent doctor`.
`pkg/vmkit` contains the request and response types used below the workspace
package.

## Lifecycle of a `microagent run`

1. The CLI parses flags into `pkg/workspace` options.
2. `pkg/workspace` prepares disks, builds the rootfs with `pkg/rootfs`, records
   verification, writes the manifest, and builds a `vmkit.Request`.
3. microagent selects the host backend.
4. The host supervisor starts the VM and tracks its lifecycle.
5. Guest init runs `--setup` then `--exec`.
6. State changes are emitted as JSON events. State files live under
   `--state-dir` (default `~/.microagent/...`).
7. On `--keep`, the workspace stays. Otherwise the workspace API cleans up
   local state.

[Run one-shot commands](/guides/one-shot-runs/) shows this flow from the
operator's side. Go callers can use the same package flow directly without the
CLI: see the [library overview](/library/) and the
[Go library reference](/library/go/).
