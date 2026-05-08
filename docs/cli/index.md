---
title: CLI reference
description: All microagent subcommands at a glance.
---

| Command | Purpose |
|---|---|
| [`run`](/cli/run/) | Boot an image and run a command, then tear down |
| [`create`](/cli/create/) | Create a named, persistent workspace |
| [`clone`](/cli/clone/) | Copy a stopped workspace into a new workspace |
| [`cp`](/cli/cp/) | Copy files into or out of stopped workspace disks |
| [`artifacts`](/cli/artifacts/) | List and retrieve declared workspace artifacts |
| [`network`](/cli/network/) | Inspect workspace network mode and port forwards |
| [`start`](/cli/start/) | Boot a stopped workspace |
| [`supervise`](/cli/supervise/) | Start and restart a workspace according to policy |
| [`halt`](/cli/halt/) | Clean disk-preserving shutdown |
| [`quarantine`](/cli/quarantine/) | Sever host-side network and mediation |
| [`stop`](/cli/stop/) | Graceful shutdown |
| [`kill`](/cli/kill/) | Hard terminate |
| [`delete`](/cli/delete/) | Remove a workspace and its state |
| [`status`](/cli/status/) | Show workspace state |
| [`result`](/cli/result/) | Show structured workspace result |
| [`ps`](/cli/ps/) | List workspaces |
| [`logs`](/cli/logs/) | Show boot/serial output |
| [`connect`](/cli/connect/) | Open the workspace console |
| [`profiles`](/cli/profiles/) | List exact named resource profiles |
| [`images`](/cli/images/) | List or prune local image records |
| [`perf`](/cli/perf/) | Measure workspace boot performance |
| [`contract`](/cli/contract/) | Print the backend-neutral runtime contract |
| [`host`](/cli/host/) | Report host backend capabilities |
| [`doctor`](/cli/doctor/) | Check the host for backend support |
| [`rootfs`](/cli/rootfs/) | Build a rootfs from an OCI image |
| [`kernel`](/cli/kernel/) | Install or verify a custom kernel |
| [`version`](/cli/version/) | Print the version |

## Workspace spec

[`microagent.yaml`](/cli/spec/) is the declarative form of `microagent create` — image, profile, restart policy, networking, mounts, mediation, and outputs in a single file you can keep in source control.

## Global flags

- `--json` — print JSON output; place before the subcommand
- `--text` — print human-readable output
- `--output <json|text>` — select output format
- `--supervisor <path>` — override the active backend supervisor path
  (`MICROAGENT_APPLEVF_SUPERVISOR` and
  `MICROAGENT_FIRECRACKER_SUPERVISOR` work too)

## Output

All commands can print JSON output. With `--json` before the subcommand (or
`MICROAGENT_OUTPUT=json`), the response matches the shape documented in the
[supervisor protocol](/protocol/). Scripts should consume JSON; humans get the
text format by default.
