# microagent-kit

Go CLI/library plus a Swift helper for running AI agents inside inspectable
Apple Virtualization.framework microVMs.

## Scope

This repository owns host-side microVM lifecycle primitives for agent
workloads:

- create, start, status, stop, kill, and delete CLI commands
- rootfs builds from OCI image references
- guest metadata and runtime identity propagation
- serial, block-device, and vsock-oriented host/guest wiring
- bounded cleanup and observable runtime state
- Apple Virtualization.framework helper protocol

## Non-goals

- Do not implement agent orchestration, planning, LLM calls, tool semantics, or
  memory.
- Do not implement policy, audit semantics, credential mediation, or
  enforcement decisions. Consumers own those.
- Do not become a general-purpose Mac VM manager. Lima, Tart, vfkit, and Lume
  already serve that space.
- Do not grow rootfs build logic into a general image scanner, signer, or
  registry management tool.

## Design rules

- Keep the public contract structured and machine-readable.
- Keep the Swift helper usable from Go, Python, Rust, Node, and shell scripts.
- Treat lifecycle events as first-class API output, not log strings.
- Preserve explicit runtime identity in requests, state files, and events.
- Keep Apple VF details behind the helper protocol.
- Fail closed on invalid runtime configuration.
- Prefer narrow protocols over shell-string execution.

## Consumer boundary

Consumers supply policy, audit meaning, identity, rootfs artifacts, and
operator intent. This project runs VM lifecycle commands and reports state.
