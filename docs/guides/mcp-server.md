---
title: Serve microagent over MCP
description: Register microagent serve mcp in Claude Code or another MCP client and drive workspaces with tools.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

By the end of this guide your coding agent can create, run, and inspect
microVM workspaces through MCP tools instead of shelling out. `microagent
serve mcp` is a stdio MCP server: the client launches it as a subprocess and
speaks JSON-RPC over stdin/stdout - no daemon, no port.

## 1. Check the host

Install `microagent` on the machine where the client will launch it, then:

```bash
microagent doctor
```

The MCP server uses the same backend and state directory (`~/.microagent/`)
as the CLI, so anything `doctor` flags will affect tools too.

## 2. Register it in your client

For Claude Code, one command:

```bash
claude mcp add --transport stdio --scope user microagent -- microagent serve mcp
```

Or, for a project-shared setup, put this in `.mcp.json` at the project root:

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

Two practical notes. If the client is a GUI app or remote session that doesn't
inherit your shell `PATH`, use the absolute path from `command -v microagent`
as the `command`. And raise the client's tool timeout where it supports one -
image pulls and rootfs builds are long operations.

Every stdio MCP client takes the same shape: `command: microagent`,
`args: ["serve", "mcp"]`. The [`serve`](/cli/serve/) reference has the exact
configuration for Codex, VS Code, Copilot CLI, and others. Don't run it as an
HTTP/SSE server or background daemon - started directly from a terminal it
just prints setup guidance and exits.

## 3. See what the agent gets

The server exposes the full substrate surface - 57 structured tools covering
workspace lifecycle (`workspace.create`, `workspace.exec`, `workspace.halt`,
...), inspection (`workspace.inspect`, `workspace.logs`, `workspace.events`),
snapshots, images, networks, volumes, copy and artifacts, host diagnostics,
and cost estimation. The MCP server only exposes the substrate operations listed above; see [Boundaries](/concepts/boundaries/) for where microagent's responsibilities end.

Destructive tools take `preview: true` to report what would happen without
doing it, and the riskiest host mutations (`kernel.install`, `rootfs.build`,
`host.networking.setup`) require a preview-then-confirm token exchange. The
[`serve`](/cli/serve/) reference lists every tool.

## 4. Try it

In Claude Code, after registering:

```text
> Use the microagent MCP server to boot an alpine microVM and run `uname -a` in it.
```

The agent calls `workspace.create`, `workspace.start`, and `workspace.exec`
and reads back structured results - exit code, base64 stdout/stderr, timing.
You can verify the transport by hand the same way any MCP client does; an
`initialize` handshake followed by `tools/call` of `microagent.ping` returns:

```text
server: {'name': 'microagent', 'version': '0.1.46'}
tools: 57 exposed
ping: {"content": [{"text": "pong", "type": "text"}]}
```

## Clean up

The MCP server holds no state of its own - the client starts and stops the
subprocess. Workspaces an agent created are ordinary workspaces; list and
remove leftovers like always:

```bash
microagent ps
microagent delete <name> --yes
```

## What's next

- **Every tool, envelope field, and preview contract** - the [`serve`](/cli/serve/) reference.
- **The runtime contract the tools expose** - [`contract`](/cli/contract/).
- **What an agent should build on top** - [build a simple agent](/guides/simple-agent/).
