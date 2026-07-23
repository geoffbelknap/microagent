---
title: Boundaries
description: Know what microagent owns and what your runtime must supply before you build on it.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-23_

`microagent` runs Linux workspaces inside microVMs, and it stops at the VM
boundary. If you are building a runtime on top of it, this is the line to keep
in mind.

microagent supplies the VM layer: kernel, rootfs conversion, lifecycle, state,
and structured CLI/MCP output. Your program supplies identity, policy,
credentials, and intent.

## In this repo

- VM commands (`run`, `create`, `start`, `status`, `halt`, `stop` (CLI aliases
  it to `halt`), `quarantine`, `pause`, `resume`, `kill`, `delete`)
- OCI image to ext4 rootfs builds
- Identity in requests and state files
- State changes as JSON
- Readiness, structured exec, structured results, and declared artifacts
- Host supervisor boundary
- MCP stdio adapter over the workspace APIs
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

## Identity, policy, and credentials stay outside

microagent transports identity; it never mints or judges it. Every request
carries an identity block that is recorded in state files and events (see
[State and identity](/concepts/state-and-identity/)), but the meaning of a
role, the decision to allow an action, and the authority behind it belong to
your control plane.

Tool mediation follows the same rule. The
[mediation channel](/guides/agents-and-mediation/) gives the guest one
declared path to your host control plane; your listener decides what each
call may do. Secrets too: microagent [delivers them](/guides/secrets/)
without persisting them, and your secret manager stays the source of truth.

If a guide asks you to write a policy check, a host listener, or a credential
fetch, that belongs outside microagent. It is not a missing feature.

## Design rules

- Public output is structured and machine-readable.
- [AX mode](/concepts/glossary/) and MCP responses are for clients, not log scraping.
- State changes are API output, not log strings.
- Identity is preserved explicitly in requests, state files, and events.
- Host details stay behind supervisor boundaries.
- Invalid VM config fails closed.
- Narrow protocols beat shell-string execution.
