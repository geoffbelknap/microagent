---
title: Boundaries
description: What microagent owns, and what it deliberately leaves to the caller.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-01_

`microagent` runs Linux workspaces inside microVMs. It stops at the VM
boundary. The caller owns policy, identity, and intent.

## In this repo

- VM commands (`run`, `create`, `start`, `status`, `halt`, `quarantine`,
  `stop`, `kill`, `delete`)
- OCI image to ext4 rootfs builds
- Identity in requests and state files
- State changes as JSON
- Readiness, structured exec, structured results, and declared artifacts
- Backend supervisor boundary
- MCP stdio adapter over the existing substrate APIs
- Firecracker supervisor implementation (Go executable)
- Apple Virtualization.framework supervisor implementation (Swift executable)
- Windows Hyper-V supervisor implementation (Go, experimental)
- State files and cleanup
- Host/guest wiring such as vsock listeners

## Outside this repo

- Planning loops
- LLM/provider calls
- Tool mediation and tool policy
- Policy decisions
- Audit meaning and retention
- Credentials and grants
- Agent frameworks and user experience

Your program supplies identity, bridge targets, policy, and intent. microagent
supplies the kernel, rootfs conversion, VM state, VM commands, and structured
adapter surfaces.

## Design rules

- Public output is structured and machine-readable.
- [AX mode](/concepts/glossary/) and MCP responses are for clients, not log scraping.
- The Apple VF supervisor stays usable from Go, Python, Rust, Node, and shell.
- State changes are API output, not log strings.
- Identity is preserved explicitly in requests, state files, and events.
- Backend details stay behind supervisor boundaries.
- Invalid VM config fails closed.
- Narrow protocols beat shell-string execution.
