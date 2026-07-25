---
title: Serve microagent over MCP
description: Register microagent serve mcp in Claude Code or another MCP client and drive workspaces with tools.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-25_

Give a coding agent microVM workspaces as tools. Register
`microagent serve mcp` in Claude Code, Claude Desktop, Codex, or any MCP
client, and the agent can create, run, and inspect workspaces without shelling
out. The server uses stdio: the client launches it as a subprocess and speaks
JSON-RPC over stdin/stdout. There is no daemon and no open port.

## 1. Check the host

Install `microagent` on the machine where the client will launch it, then:

```bash
microagent doctor
```

The MCP server uses the same backend and state directory as the CLI - the
state directory is where microagent keeps VM disks and metadata
(`~/.microagent/` by default) - so anything `doctor` flags will affect tools
too.

## 2. Register it in your client

### Claude Code

One command:

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

**Check it worked:** run `claude mcp list` in a terminal, or open the `/mcp`
panel inside Claude Code, and confirm `microagent` is connected. If it is
listed but failing, the usual cause is `PATH`: replace `microagent` in the
configuration with the absolute path from `command -v microagent`.

### Claude Desktop

Claude Desktop reads MCP servers from `claude_desktop_config.json`:

- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`

Add microagent with the **absolute** path to the binary - Claude Desktop is a
GUI app and does not inherit your shell `PATH`:

```json
{
  "mcpServers": {
    "microagent": {
      "command": "/usr/local/bin/microagent",
      "args": ["serve", "mcp"]
    }
  }
}
```

microagent runs on the same machine as Claude Desktop, so `microagent doctor`
must pass there first. Save the file and restart the app.

**Check it worked:** after the restart, look for the tools icon in the chat
input area - microagent's tools should appear in its list. If they don't, the
`command` path is almost always the problem; verify it with
`command -v microagent`.

### Other clients

Every stdio MCP client takes the same shape: `command: microagent`,
`args: ["serve", "mcp"]`, absolute path if the client doesn't inherit your
shell `PATH`. The [`serve`](/cli/serve/) reference has the exact configuration
for Codex, VS Code, Copilot CLI, and others. Don't run it as an HTTP/SSE
server or background daemon - started directly from a terminal it just prints
setup guidance and exits.

One more practical note: raise the client's tool timeout where it supports one
(the Claude Code snippet above uses 600000 ms). The first `workspace.create`
for a new image downloads that image, so expect a minute or more; later boots
of the same image are fast. Image pulls and rootfs builds are similarly long
the first time.

## 3. Try it

In Claude Code, after registering:

```text
> Use the microagent MCP server to boot an alpine microVM and run `uname -a` in it.
```

The agent calls `workspace.create`, `workspace.start`, and `workspace.exec`,
then reads back the exit code, stdout/stderr, and timing.
You can verify the transport by hand: pipe a single `initialize` request into
the server and it answers on stdout, then exits when stdin closes:

```bash
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}' | microagent serve mcp
```

```text
{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"tools":{}},"protocolVersion":"2025-06-18","serverInfo":{"name":"microagent","version":"<version>"}}}
```

## 4. What to try next

The server exposes more than fifty tools. The headline for agent workloads is
`workspace.dispatch`: it runs one task in a fresh, isolated, single-use
microVM under egress guardrails, tears it down, and returns the result plus a
summary of what the workspace reached on the network. Around it sit workspace
lifecycle (`workspace.create`, `workspace.exec`, `workspace.halt`, ...),
inspection (`workspace.inspect`, `workspace.logs`, `workspace.events`,
`workspace.egress`), snapshots, images, networks, volumes, copy and artifacts,
host diagnostics, and cost estimation.

The server stops at VM operations: it does not plan, call an LLM, or interpret
audit records. Its tools can configure credential brokering and egress policy
for the mediator, but the MCP server itself never holds secrets and never
makes policy decisions. See [Boundaries](/concepts/boundaries/) for the line
microagent does not cross.

Destructive tools take `preview: true` to report what would happen without
doing it, and the riskiest host mutations (`kernel.install`, `rootfs.build`)
require a preview-then-confirm token exchange. The [`serve`](/cli/serve/)
reference lists every tool.

Some prompts that exercise the interesting parts:

```text
> Dispatch this script into a throwaway microVM that is allowed to reach only
  api.example.com, and show me which hosts it tried to contact.
```

```text
> Snapshot workspace dev as before-upgrade, run the upgrade, and restore the
  snapshot if the tests fail.
```

```text
> List my microagent workspaces and delete the stopped ones - preview the
  deletions first.
```

## Permissions

When your client asks whether to allow a tool call, keep in mind that
`workspace.delete` over MCP deletes immediately once the call is approved -
the CLI's interactive confirmation is bypassed, because the client's approval
is the confirmation. Don't auto-allow the delete tools (`workspace.delete`,
`volume.delete`, `snapshot.delete`, `images.delete`, `images.prune`), and for
anything you'd miss, ask the agent to call the tool with `preview: true` first
and show you the result.

## Troubleshooting

- **The server doesn't appear in the client.** The binary isn't on the `PATH`
  the client sees. Use the absolute path from `command -v microagent` as the
  `command`, then restart the client.
- **Every tool call fails the same way.** The host backend is the problem, not
  MCP. Run `microagent doctor` on the machine where the client launches the
  server and fix what it flags.
- **Tool calls time out.** First-time image pulls, rootfs builds, and the
  first boot of a new image are long operations. Raise the client's MCP tool
  timeout and retry - subsequent calls reuse the downloaded image.

## Clean up

The MCP server holds no state of its own - the client starts and stops the
subprocess. Workspaces an agent created are ordinary workspaces; list and
remove leftovers like always:

```bash
microagent list
microagent delete <name> --yes
```

## Related

- [`serve`](/cli/serve/) lists every MCP tool and preview-confirm flow.
- [`contract`](/cli/contract/) prints the runtime fields tools rely on.
- [Build a simple agent](/guides/simple-agent/) shows what to run inside a workspace.
