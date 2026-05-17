---
title: Boundaries
description: What microagent owns, and what it deliberately leaves to the caller.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-11_

`microagent` runs Linux workspaces inside microVMs. It stops at the VM
boundary. Other systems own policy, identity, and intent.

## In this repo

- VM commands (`run`, `create`, `start`, `status`, `halt`, `quarantine`,
  `stop`, `kill`, `delete`)
- OCI image to ext4 rootfs builds
- Identity in requests and state files
- State changes as JSON
- Backend supervisor boundary
- Firecracker supervisor implementation (Go executable)
- Apple Virtualization.framework supervisor implementation (Swift executable)
- State files and cleanup
- Host/guest wiring such as vsock listeners

## Outside this repo

- Planning loops
- LLM/provider calls
- Tool mediation
- Policy decisions
- Audit meaning and retention
- Credentials and grants
- User experience

Your program supplies identity and bridge targets. microagent provides the
kernel, rootfs conversion, VM state, and VM commands without taking over
policy.

## Design rules

- Public output is structured and machine-readable.
- The Apple VF supervisor stays usable from Go, Python, Rust, Node, and shell.
- State changes are API output, not log strings.
- Identity is preserved explicitly in requests, state files, and events.
- Backend details stay behind supervisor boundaries.
- Invalid VM config fails closed.
- Narrow protocols beat shell-string execution.
