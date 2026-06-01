---
title: CLI reference
description: All microagent subcommands at a glance.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

| Command | Purpose |
|---|---|
| [`run`](/cli/run/) | Boot an image and run a command, then tear down |
| [`create`](/cli/create/) | Create a named, persistent workspace |
| [`apply`](/cli/apply/) | Apply supported workspace spec changes without rebuilding |
| [`clone`](/cli/clone/) | Copy a stopped workspace into a new workspace |
| [`cp`](/cli/cp/) | Copy files into or out of stopped workspace disks |
| [`artifacts`](/cli/artifacts/) | List and retrieve declared workspace artifacts |
| [`network`](/cli/network/) | Inspect declared network intent and runtime network state |
| [`start`](/cli/start/) | Boot a stopped workspace |
| [`supervise`](/cli/supervise/) | Start and restart a workspace according to policy |
| [`halt`](/cli/halt/) | Clean disk-preserving shutdown |
| [`quarantine`](/cli/quarantine/) | Sever host-side network and mediation |
| [`stop`](/cli/stop/) | Graceful shutdown |
| [`kill`](/cli/kill/) | Hard terminate |
| [`delete`](/cli/delete/) | Remove a workspace and its state |
| [`rm`](/cli/delete/) | Alias for `delete` |
| [`status`](/cli/status/) | Show workspace state |
| [`inspect`](/cli/status/) | Alias for `status` with JSON output |
| [`result`](/cli/result/) | Show structured workspace result |
| [`ps`](/cli/ps/) | List workspaces |
| [`logs`](/cli/logs/) | Show boot/serial output |
| [`events`](/cli/events/) | Show or stream the lifecycle event history |
| [`connect`](/cli/connect/) | Open the workspace console |
| [`exec`](/cli/exec/) | Run a structured command in a workspace |
| [`profiles`](/cli/profiles/) | List exact named resource profiles |
| [`images`](/cli/images/) | List or prune local image records |
| [`prune`](/cli/prune/) | Prune stale records and optional reusable image baselines |
| [`perf`](/cli/perf/) | Measure workspace boot performance |
| [`serve`](/cli/serve/) | Serve machine-readable agent endpoints |
| [`contract`](/cli/contract/) | Print the backend-neutral runtime contract |
| [`host`](/cli/host/) | Report host backend capabilities |
| [`doctor`](/cli/doctor/) | Check the host for backend support |
| [`rootfs`](/cli/rootfs/) | Build a rootfs from an OCI image |
| [`kernel`](/cli/kernel/) | Install or verify a custom kernel |
| [`version`](/cli/version/) | Print the version |

## Container-style convenience

`microagent run` accepts both the explicit `--image IMAGE --exec "cmd"` form and
the shorter `microagent run IMAGE [COMMAND ARG...]` form. For flags that map
cleanly onto a microVM, common aliases are available: `-e` for `--env`, `-p` for
`--publish`, `-v`/`--volume` for tar bundles and ext4 disk images, `--name`, and
`--rm`.

Features that do not map cleanly to a microVM boundary are not implemented:
container-engine APIs, compose projects, pods, privileged mode, namespace flags,
devices, host directory bind mounts, and named volumes. When those inputs are
recognized, microagent returns targeted guidance rather than silently changing
their meaning.

## Workspace spec

[`microagent.yaml`](/cli/spec/) is the declarative form of `microagent create` - image, profile, restart policy, networking, mounts, mediation, and outputs in a single file you can keep in source control.

## Global flags

These flags are recognized before the subcommand and apply across commands that
produce output. Subcommand pages link back here rather than repeat them.

- `--json` - print structured JSON output; place before the subcommand
- `--text` - print human-readable output
- `--output <json|text>` - select output format
- `--mode <ux|ax>` - select the output mode. `ux` is the default
  human-oriented mode; `ax` is the agent mode, which forces JSON output and
  emits structured error envelopes on failure. `MICROAGENT_MODE` sets the same
  value (`ux`/`human`/`text` map to UX; `ax`/`agent`/`json` map to AX). `--mode
  ax` implies `--json`; when neither is set, `MICROAGENT_OUTPUT=json|text`
  selects the format and output otherwise follows whether stdout is a terminal.
- `--supervisor <path>` - override the installed host backend supervisor path
  (`MICROAGENT_APPLEVF_SUPERVISOR` and
  `MICROAGENT_FIRECRACKER_SUPERVISOR` work too)

## Output

All commands can print JSON output. With `--json` before the subcommand (or
`MICROAGENT_OUTPUT=json`), the response matches the shape documented in the
[supervisor protocol](/protocol/). Scripts should consume JSON; humans get the
text format by default.
