---
title: CLI reference
description: All microagent subcommands at a glance.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-23_

New to the vocabulary? See the [glossary](/concepts/glossary/).

## Which command do I want?

| I want to... | Use |
|---|---|
| Run something once and throw it away | [`run`](/cli/run/) |
| Run one task and see what it touched on the network | [`dispatch`](/cli/dispatch/) |
| Keep a workspace around between boots | [`create`](/cli/create/), then [`start`](/cli/start/) |
| Get a shell inside a workspace | [`connect`](/cli/connect/) |
| Run a command inside and get its exit code | [`exec`](/cli/exec/) |
| Copy files in or out | [`cp`](/cli/cp/) |
| See saved workspaces | [`list`](/cli/list/) or `ls` |
| See what's running | [`ps`](/cli/ps/) |
| Dig into one workspace | [`status`](/cli/status/) |
| Block until a run finishes | [`wait`](/cli/wait/) or [`start --wait`](/cli/start/) |
| See what the VM printed at boot | [`logs`](/cli/logs/) |
| Get the structured result of a run | [`result`](/cli/result/) |
| Park it / shut it down / force it | [`halt`](/cli/halt/) / [`stop`](/cli/stop/) / [`kill`](/cli/kill/) |
| Freeze it in place, memory and all | [`pause`](/cli/pause/) / [`resume`](/cli/resume/) |
| Checkpoint or fork it | [`snapshot`](/cli/snapshot/), [`clone`](/cli/clone/) |
| Get rid of it | [`delete`](/cli/delete/) |
| Figure out why nothing boots | [`doctor`](/cli/doctor/) |

## All commands

| Command | Purpose |
|---|---|
| [`init`](/cli/init/) | Scaffold a starter agent project |
| [`run`](/cli/run/) | Boot an image and run a command, then tear down |
| [`dispatch`](/cli/dispatch/) | Run one task in a single-use workspace with an egress audit receipt |
| [`create`](/cli/create/) | Create a named, persistent workspace |
| [`apply`](/cli/apply/) | Apply supported workspace spec changes without rebuilding |
| [`clone`](/cli/clone/) | Copy a stopped workspace into a new workspace |
| [`commit`](/cli/commit/) | Snapshot a stopped workspace rootfs into an OCI image |
| [`cp`](/cli/cp/) | Copy files into or out of stopped workspace disks |
| [`artifact`](/cli/artifact/) | List and retrieve declared workspace artifacts |
| [`network`](/cli/network/) | Inspect declared network intent and runtime network state |
| [`model`](/cli/model/) | Download and manage local HuggingFace GGUF model files |
| [`volume`](/cli/volume/) | Manage named volumes - VM-independent ext4 disks attached by name |
| [`start`](/cli/start/) | Boot a stopped workspace |
| [`supervise`](/cli/supervise/) | Start and restart a workspace according to policy |
| [`halt`](/cli/halt/) | Clean disk-preserving shutdown |
| [`quarantine`](/cli/quarantine/) | Sever host-side network and mediation |
| [`pause`](/cli/pause/) | Freeze a running workspace's vCPUs, preserving memory and disk |
| [`resume`](/cli/resume/) | Thaw a paused workspace back to running |
| [`stop`](/cli/stop/) | Graceful shutdown |
| [`kill`](/cli/kill/) | Hard terminate |
| [`delete`](/cli/delete/) | Remove a workspace and its state |
| [`status`](/cli/status/) | Show workspace state |
| [`wait`](/cli/wait/) | Block until a workspace's run finishes |
| [`result`](/cli/result/) | Show structured workspace result |
| [`list`](/cli/list/) | List saved workspaces (`ls` alias) |
| [`ps`](/cli/ps/) | List running workspaces |
| [`logs`](/cli/logs/) | Show boot/serial output |
| [`events`](/cli/events/) | Show or stream the lifecycle event history |
| [`egress`](/cli/egress/) | Show or stream the egress mediator's audit decisions |
| [`stats`](/cli/stats/) | Show or stream workspace resource usage |
| [`snapshot`](/cli/snapshot/) | Create, list, or remove workspace snapshots |
| [`secret`](/cli/secret/) | Resolve and validate secret references |
| [`connect`](/cli/connect/) | Open the workspace console |
| [`exec`](/cli/exec/) | Run a structured command in a workspace |
| [`profiles`](/cli/profiles/) | List exact named resource profiles |
| [`image`](/cli/image/) | Manage local image records |
| [`registry`](/cli/registry/) | Store credentials for private OCI registries |
| [`perf`](/cli/perf/) | Measure workspace boot performance |
| [`serve`](/cli/serve/) | Run the MCP stdio server for agent clients |
| [`contract`](/cli/contract/) | Print the runtime fields integrations rely on |
| [`host`](/cli/host/) | Report host backend capabilities |
| [`doctor`](/cli/doctor/) | Check the host for backend support |
| [`rootfs`](/cli/rootfs/) | Build a rootfs from an OCI image |
| [`kernel`](/cli/kernel/) | Install or verify a custom kernel |
| [`gc`](/cli/gc/) | Reap dead VM processes and stale workspace state |
| [`version`](/cli/version/) | Print the version |

## Container-style convenience

`microagent run` accepts both the explicit `--image IMAGE --exec "cmd"` form and
the shorter `microagent run IMAGE [COMMAND ARG...]` form. For flags that map
cleanly onto a microVM, common aliases are available: `-e` for `--env`, `-p` for
`--publish`, `-v`/`--volume` for [named volumes](/cli/volume/), tar bundles, and
ext4 disk images, `--name`, and `--rm`.

Some Docker-style inputs do not map to a microVM boundary - privileged mode,
namespace flags, devices, and host directory bind mounts. When `microagent run`
recognizes one of these, it returns targeted guidance rather than silently
changing its meaning.

## Workspace spec

[`microagent.yaml`](/cli/spec/) is the declarative form of `microagent create` - image, profile, restart policy, networking, mounts, mediation, and outputs in a single file you can keep in source control.

## Global flags

These flags may appear before or after the subcommand and apply across
commands that produce output. Subcommand pages link back here rather than
repeat them.

For [`run`](/cli/run/), [`dispatch`](/cli/dispatch/), and [`exec`](/cli/exec/),
place them before the image or workspace name so guest flags are never
touched. microagent's own flags are recognized before and after that
positional too - a flag-form invocation like `run --image IMAGE --exec "cmd"
--json` still extracts the trailing `--json` because the parser tracks which
flags take a value - but once a bare positional guest command begins (`run
IMAGE COMMAND ARGS...`), everything from there is passed through to the guest
verbatim, so a global flag placed after it will not be extracted.

- `--json` - print structured JSON output
- `--text` - print human-readable output
- `--output <json|text>` - select output format
- `--mode <ux|ax>` - select the output mode. `ux` is the default
  human-oriented mode; `ax` is the agent mode, which forces JSON output and
  emits structured error envelopes on failure. `MICROAGENT_MODE` sets the same
  value (`ux`/`human`/`text` map to UX; `ax`/`agent`/`json` map to AX). `--mode
  ax` implies `--json`; when neither is set, `MICROAGENT_OUTPUT=json|text`
  selects the format and output otherwise follows whether stdout is a terminal.

In AX mode, any command failure is written as a structured error envelope on
stdout - command pages say "in AX mode a failure is written as a structured
error envelope" and mean exactly this.

`--supervisor <path>` overrides the installed host backend supervisor path
(`MICROAGENT_APPLEVF_SUPERVISOR` and `MICROAGENT_FIRECRACKER_SUPERVISOR` work
too), but it is **not** a global flag - pass it after the subcommand, on the
commands that accept it.

## Output

All commands can print JSON output. With `--json` before the subcommand (or
`MICROAGENT_OUTPUT=json`), the response uses the same structured result shape
the Go library and MCP adapter use. Scripts should consume JSON; humans get
the text format by default.
