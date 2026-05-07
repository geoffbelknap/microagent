---
title: CLI reference
description: All microagent subcommands at a glance.
---

| Command | Purpose |
|---|---|
| [`run`](run.md) | Boot an image and run a command, then tear down |
| [`create`](create.md) | Create a named, persistent workspace |
| [`clone`](clone.md) | Copy a stopped workspace into a new workspace |
| [`cp`](cp.md) | Copy files into or out of stopped workspace disks |
| [`artifacts`](artifacts.md) | List and retrieve declared workspace artifacts |
| [`network`](network.md) | Inspect workspace network mode and port forwards |
| [`start`](start.md) | Boot a stopped workspace |
| [`supervise`](supervise.md) | Start and restart a workspace according to policy |
| [`halt`](halt.md) | Clean disk-preserving shutdown |
| [`quarantine`](quarantine.md) | Sever host-side network and mediation |
| [`stop`](stop.md) | Graceful shutdown |
| [`kill`](kill.md) | Hard terminate |
| [`delete`](delete.md) | Remove a workspace and its state |
| [`status`](status.md) | Show workspace state |
| [`result`](result.md) | Show structured workspace result |
| [`ps`](ps.md) | List workspaces |
| [`logs`](logs.md) | Show boot/serial output |
| [`connect`](connect.md) | Open the workspace console |
| [`profiles`](profiles.md) | List exact named resource profiles |
| [`images`](images.md) | List or prune local image records |
| [`perf`](perf.md) | Measure workspace boot performance |
| [`contract`](contract.md) | Print the backend-neutral runtime contract |
| [`host`](host.md) | Report host backend capabilities |
| [`doctor`](doctor.md) | Check the host for backend support |
| [`rootfs`](rootfs.md) | Build a rootfs from an OCI image |
| [`kernel`](kernel.md) | Install or verify a custom kernel |
| [`version`](version.md) | Print the version |

## Workspace spec

[`microagent.yaml`](spec.md) is the declarative form of `microagent create` — image, profile, restart policy, networking, mounts, mediation, and outputs in a single file you can keep in source control.

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
[supervisor protocol](../protocol/index.md). Scripts should consume JSON; humans get the
text format by default.
