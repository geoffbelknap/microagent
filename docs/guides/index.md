---
title: Guides
description: Pick the thing you want to do and follow the steps.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-13_

These guides are for doing the work, not memorizing flags. Each one starts
with a task, shows runnable commands, and points to the CLI reference when the
details matter.

## Run things

- [Run one-shot commands](one-shot-runs.md) - boot a microVM, run a command, tear it down. Setup steps, env vars, artifacts, timeouts.
- [Keep a persistent workspace](persistent-workspaces.md) - create a named workspace and walk the create, start, halt, connect, delete lifecycle.
- [Run a service](run-a-service.md) - run Postgres in a workspace with a published port, a named volume for data, and a restart policy.

## Move data

- [Use volumes and move data](volumes-and-data.md) - named volumes, attached disks, tar bundles, and `cp` in and out of stopped workspaces.
- [Deliver secrets](secrets.md) - get credentials into the guest without writing them to disk, plus on-demand fetch and the audit log.

## Save and share state

- [Snapshot and fork workspaces](snapshots-and-forking.md) - checkpoint a running workspace, restore it in place, or fork copies from one snapshot.

## Connect things

- [Networking](networking.md) - give a workspace outbound access and publish a guest port back to the host.
- [Allowlist and passthrough egress](egress-allowlist.md) - confine a workspace to a known set of destinations with `--egress-lock-allowlist`, and let cert-pinned endpoints through with passthrough.
- [Serve microagent over MCP](mcp-server.md) - register `microagent serve mcp` in Claude Code or another MCP client and drive workspaces with tools.

## Build an agent

- [Build a simple agent](simple-agent.md) - a one-shot agent that takes a request, calls Claude under operator-supplied constraints, and writes a result.
- [Build agents on the mediation channel](agents-and-mediation.md) - the guest-to-host vsock contract: declare it, listen on the host, loop in the agent.
