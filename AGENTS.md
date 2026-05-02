# microagent-vmkit

Toolkit and CLI for running AI agents inside inspectable microVMs.

## Scope

This repository owns host-side microVM lifecycle primitives for agent
workloads:

- prepare, start, stop, kill, inspect, delete, and event streaming
- guest metadata and runtime identity propagation
- serial, block-device, and vsock-oriented host/guest wiring
- bounded cleanup and observable runtime state
- Apple Virtualization.framework as the first backend

## Non-Goals

- Do not implement agent orchestration, planning, LLM calls, tool semantics, or
  memory.
- Do not implement policy, audit semantics, credential mediation, or
  enforcement decisions. Consumers own those.
- Do not become a general-purpose Mac VM manager. Lima, Tart, vfkit, and Lume
  already serve that space.
- Do not own OCI image unpacking or ext4 rootfs construction. Use
  `microvm-rootfs` for that boundary.

## Design Rules

- Keep the public contract structured and machine-readable.
- Treat lifecycle events as first-class API output, not log strings.
- Preserve explicit runtime identity in requests, state files, and events.
- Keep backend-specific details behind backend adapters.
- Fail closed on invalid runtime configuration.
- Prefer narrow protocols over shell-string execution.

## Consumer Boundary

Consumers supply policy, audit meaning, identity, rootfs artifacts, and
operator intent. This project realizes VM lifecycle and reports state.
