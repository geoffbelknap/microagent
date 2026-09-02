---
title: CLI reference
description: All microagent subcommands at a glance.
---

<!-- docs-last-updated -->
_Last updated: 2026-08-16_

New to the vocabulary? See the [glossary](../concepts/glossary.md).

## Which command do I want?

| I want to... | Use |
|---|---|
| Run something once and throw it away | [`run`](run.md) |
| Run one task and see what it touched on the network | [`dispatch`](dispatch.md) |
| Keep a workspace around between boots | [`create`](create.md), then [`start`](start.md) |
| Get a shell inside a workspace | [`connect`](connect.md) |
| Run a command inside and get its exit code | [`exec`](exec.md) |
| Copy files in or out | [`cp`](cp.md) |
| See saved workspaces | [`list`](list.md) or `ls` |
| See what's running | [`ps`](ps.md) |
| Dig into one workspace | [`status`](status.md) |
| Block until a run finishes | [`wait`](wait.md) or [`start --wait`](start.md) |
| See what the VM printed at boot | [`logs`](logs.md) |
| Get the structured result of a run | [`result`](result.md) |
| Park it / shut it down / force it | [`halt`](halt.md) (alias `stop`) / [`kill`](kill.md) |
| Freeze it in place, memory and all | [`pause`](pause.md) / [`resume`](resume.md) |
| Checkpoint or fork it | [`snapshot`](snapshot.md), [`clone`](clone.md) |
| Get rid of it | [`delete`](delete.md) |
| Figure out why nothing boots | [`doctor`](doctor.md) |

## All commands

| Command | Purpose |
|---|---|
| [`init`](init.md) | Scaffold a starter agent project |
| [`run`](run.md) | Boot an image and run a command, then tear down |
| [`dispatch`](dispatch.md) | Run one task in a single-use workspace with an egress audit receipt |
| [`create`](create.md) | Create a named, persistent workspace |
| [`apply`](apply.md) | Apply supported workspace spec changes without rebuilding |
| [`clone`](clone.md) | Copy a stopped workspace into a new workspace |
| [`commit`](commit.md) | Snapshot a stopped workspace rootfs into an OCI image |
| [`resize`](resize.md) | Grow or shrink a stopped workspace's rootfs disk |
| [`cp`](cp.md) | Copy files into or out of stopped workspace disks |
| [`artifact`](artifact.md) | List and retrieve declared workspace artifacts |
| [`network`](network.md) | Inspect declared network intent and runtime network state |
| [`model`](model.md) | Download and manage local HuggingFace GGUF model files |
| [`volume`](volume.md) | Manage named volumes - VM-independent ext4 disks attached by name |
| [`start`](start.md) | Boot a stopped workspace |
| [`supervise`](supervise.md) | Start and restart a workspace according to policy |
| [`halt`](halt.md) | Clean disk-preserving shutdown (`stop` alias) |
| [`quarantine`](quarantine.md) | Freeze, sever authority, capture, and stop into custody |
| [`pause`](pause.md) | Freeze a running workspace's vCPUs, preserving memory and disk |
| [`resume`](resume.md) | Thaw a paused workspace back to running |
| [`kill`](kill.md) | Hard terminate |
| [`delete`](delete.md) | Remove a workspace and its state |
| [`status`](status.md) | Show workspace state |
| [`wait`](wait.md) | Block until a workspace's run finishes |
| [`result`](result.md) | Show structured workspace result |
| [`list`](list.md) | List saved workspaces (`ls` alias) |
| [`ps`](ps.md) | List running workspaces |
| [`logs`](logs.md) | Show boot/serial output |
| [`events`](events.md) | Show or stream the lifecycle event history |
| [`egress`](egress.md) | Show or stream the egress mediator's audit decisions |
| [`stats`](stats.md) | Show or stream workspace resource usage |
| [`snapshot`](snapshot.md) | Create, list, or remove workspace snapshots |
| [`secret`](secret.md) | Resolve and validate secret references |
| [`connect`](connect.md) | Open the workspace console |
| [`exec`](exec.md) | Run a structured command in a workspace |
| [`profiles`](profiles.md) | List exact named resource profiles |
| [`image`](image.md) | Manage local image records |
| [`registry`](registry.md) | Store credentials for private OCI registries |
| [`perf`](perf.md) | Measure workspace boot performance |
| [`serve`](serve.md) | Run the MCP stdio server for agent clients |
| [`contract`](contract.md) | Print the runtime fields integrations rely on |
| [`host`](host.md) | Report host backend capabilities |
| [`doctor`](doctor.md) | Check the host for backend support |
| [`rootfs`](rootfs.md) | Build a rootfs from an OCI image |
| [`kernel`](kernel.md) | Install or verify a custom kernel |
| [`gc`](gc.md) | Reap dead VM processes and stale workspace state |
| [`version`](version.md) | Print the version |

## Container-style convenience

`microagent run` accepts both the explicit `--image IMAGE --exec "cmd"` form and
the shorter `microagent run IMAGE [COMMAND ARG...]` form. For flags that map
cleanly onto a microVM, common aliases are available: `-e` for `--env`, `-p` for
`--publish`, `-v`/`--volume` for [named volumes](volume.md), tar bundles, and
ext4 disk images, `--name`, and `--rm`.

Some Docker-style inputs do not map to a microVM boundary - privileged mode,
namespace flags, devices, and host directory bind mounts. When `microagent run`
recognizes one of these, it returns targeted guidance rather than silently
changing its meaning.

## Workspace spec

[`microagent.yaml`](spec.md) is the declarative form of `microagent create` - image, profile, restart policy, networking, mounts, mediation, and outputs in a single file you can keep in source control.

## Global flags

These flags may appear before or after the subcommand and apply across
commands that produce output. Subcommand pages link back here rather than
repeat them.

For [`run`](run.md), [`dispatch`](dispatch.md), and [`exec`](exec.md),
place them before the image or workspace name so guest flags are never
touched. In flag form (`run --image IMAGE --exec "cmd" --json`), the parser
tracks which flags take a value, so a trailing `--json` is still extracted.
In positional form (`run IMAGE COMMAND ARGS...`), everything after the image
is passed to the guest verbatim - a global flag placed there is not
extracted.

The CLI has one interaction model for human operators. Output can be rendered
as text or serialized as JSON for scripts:

- `--output <json|text>` - select output format
- `--json` - sugar for `--output json`
- `--progress <auto|plain|off>` - select animated, non-animated, or disabled
  human progress
- `--no-color` - disable the ANSI color some text output uses on state words
  (`failed`, `running`, `ready`, `ok`, `PASS`, `WARN`, `quarantined`,
  `paused`). Color is a redundant channel only: the word itself is always
  printed regardless. It is applied only on a TTY, and is also disabled by
  the `NO_COLOR` environment variable; JSON output never carries color.

Format is resolved in this order - the first one set wins:

| Precedence | Source |
|---|---|
| 1 | An explicit `--output`/`--json` flag |
| 2 | `MICROAGENT_OUTPUT=json\|text` |
| 3 | TTY detection (`text` on a terminal, `json` otherwise) |

### Progress and accessibility

Progress is human presentation, not result data. In the default `auto` mode,
an interactive terminal gets one animated current line with aligned elapsed
time and a stable completion line. Redirected text output gets bounded plain
phase transitions with no ANSI controls or spinner frames. Progress is written
to stderr; command results and streamed guest data remain on stdout.

Use `plain` for a screen reader, reduced-motion terminal, or stable operator
logs. Use `off` when automation wants text results without progress:

```bash
microagent --progress plain start research
MICROAGENT_PROGRESS=off microagent --output text rootfs build --image alpine --out rootfs.ext4
```

A plain rootfs build uses stable phase lines and one completion line:

```text
• Build rootfs · fetching manifest
• Build rootfs · building ext4 image
✓ [ 1.42s] Build rootfs
```

The explicit flag takes precedence over `MICROAGENT_PROGRESS`. Supported values
for both are `auto`, `plain`, and `off`. JSON always disables human progress,
regardless of this setting. MCP protocol output and its AX responses remain
typed and never contain terminal presentation; a person launching the server
from a terminal may see only its startup acknowledgement on stderr.

Plain progress is designed to be readable and bounded in logs, but its wording
is not a machine API. Agent clients that need intermediate state should consume
typed operation events when a command exposes them, rather than parse terminal
text.

The removed `--mode ux|ax` profiles are not accepted. Scripts should use
`--json`; agent clients should use [`microagent serve mcp`](serve.md).
See [`MIGRATION.md`](https://github.com/geoffbelknap/microagent/blob/main/MIGRATION.md).

`--supervisor <path>` overrides the installed host backend supervisor path
(`MICROAGENT_APPLEVF_SUPERVISOR` and `MICROAGENT_FIRECRACKER_SUPERVISOR` work
too), but it is not a global flag - pass it after the subcommand, on the
commands that accept it.

## Output

All commands can print JSON output. With `--json` before the subcommand (or
`MICROAGENT_OUTPUT=json`), the response uses the same structured result shape
the Go library and MCP adapter use. Scripts should consume JSON; humans get
the text format by default.

## Errors and exit codes

Failures carry the same classification MCP clients receive. In text mode the
message is followed by an indented remediation line when one is known. With
JSON output explicitly selected, the full structured error — `kind`,
`message`, `remediation`, `retryable` — is written to stderr as one line
(stderr, because stdout may already hold the command's own payload).

Exit codes say what to do next, not just that something happened:

| Exit | Meaning |
|---|---|
| `0` | success (including asking for help) |
| `1` | the operation ran and failed; fix something before retrying |
| `2` | usage error — the command line itself was wrong |
| `75` | transient failure (`retryable: true`); retrying may succeed unchanged |

Commands that run guest code (`exec`, `run`, `dispatch`) pass the guest's own
exit code through; the codes above apply to microagent-level failures.
