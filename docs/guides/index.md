---
title: Guides
description: Task-shaped walkthroughs - pick the thing you want to do and follow the steps.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-23_

Each guide takes one task from start to finish with runnable commands and real
output. If you want flag-by-flag detail instead, see the [CLI
reference](/cli/); for the ideas behind the commands, see
[Concepts](/concepts/architecture/).

## Run things

- [Run one-shot commands](/guides/one-shot-runs/) - boot a microVM, run a command, tear it down. Setup steps, env vars, artifacts, timeouts.
- [Keep a persistent workspace](/guides/persistent-workspaces/) - create a named workspace and walk the create, start, halt, connect, delete lifecycle.
- [Run a service](/guides/run-a-service/) - run Postgres in a workspace with a published port, a named volume for data, and a restart policy.

## Move data

- [Use volumes and move data](/guides/volumes-and-data/) - named volumes, attached disks, tar bundles, and `cp` in and out of stopped workspaces.
- [Deliver secrets](/guides/secrets/) - get credentials into the guest without writing them to disk, plus on-demand fetch and the audit log.

## Save and share state

- [Snapshot and fork workspaces](/guides/snapshots-and-forking/) - checkpoint a running workspace, restore it in place, or fork copies from one snapshot.

## Connect things

- [Networking](/guides/networking/) - give a workspace outbound access and publish a guest port back to the host.
- [Allowlist and passthrough egress](/guides/egress-allowlist/) - confine a workspace to a known set of destinations with `strict`, and let cert-pinned endpoints through with passthrough.
- [Serve microagent over MCP](/guides/mcp-server/) - register `microagent serve mcp` in Claude Code or another MCP client and drive workspaces with tools.

## Build an agent

- [Build a simple agent](/guides/simple-agent/) - a one-shot agent that takes a request, calls Claude under operator-supplied constraints, and writes a result.
- [Build agents on the mediation channel](/guides/agents-and-mediation/) - the guest-to-host vsock contract: declare it, listen on the host, loop in the agent.
