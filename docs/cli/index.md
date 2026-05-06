---
title: CLI reference
description: All microagent subcommands at a glance.
---

| Command | Purpose |
|---|---|
| [`run`](/cli/run/) | Boot an image and run a command, then tear down |
| [`create`](/cli/create/) | Create a named, persistent workspace |
| [`start`](/cli/start/) | Boot a stopped workspace |
| [`stop`](/cli/stop/) | Graceful shutdown |
| [`kill`](/cli/kill/) | Hard terminate |
| [`delete`](/cli/delete/) | Remove a workspace and its state |
| [`status`](/cli/status/) | Show workspace state |
| [`ps`](/cli/ps/) | List workspaces |
| [`logs`](/cli/logs/) | Show boot/serial output |
| [`connect`](/cli/connect/) | Open the workspace console |
| [`doctor`](/cli/doctor/) | Check the host for backend support |
| [`rootfs`](/cli/rootfs/) | Build a rootfs from an OCI image |
| [`kernel`](/cli/kernel/) | Install or verify a custom kernel |
| [`version`](/cli/version/) | Print the version |

## Global flags

- `--json` — print JSON output
- `--text` — print human-readable output
- `--output <json|text>` — select output format
- `--supervisor <path>` — override the active backend supervisor path
  (`MICROAGENT_APPLEVF_SUPERVISOR` and
  `MICROAGENT_FIRECRACKER_SUPERVISOR` work too)

## Output

All commands return structured output. With `--json` (or
`MICROAGENT_OUTPUT=json`), the response matches the shape documented in the
[supervisor protocol](/protocol/). Scripts should consume JSON; humans get the
text format by default.
