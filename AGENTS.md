# microagent-kit

Go CLI/library plus a Swift helper for running Linux workspaces inside
microVMs.

## Scope

This repository owns the VM pieces:

- create, start, status, stop, kill, and delete commands
- rootfs builds from OCI images
- guest metadata and identity propagation
- serial, block-device, and vsock wiring
- cleanup and state files
- Apple Virtualization.framework helper protocol

## Non-goals

- Do not implement orchestration, planning, LLM calls, tools, or memory.
- Do not implement policy, audit meaning, credential mediation, or enforcement
  decisions. Consumers own those.
- Do not become a general-purpose Mac VM manager. Lima, Tart, vfkit, and Lume
  already serve that space.
- Do not grow rootfs build logic into a general image scanner, signer, or
  registry management tool.

## Design rules

- Keep public output structured and machine-readable.
- Keep the Swift helper usable from Go, Python, Rust, Node, and shell scripts.
- Treat state changes as API output, not log strings.
- Preserve explicit identity in requests, state files, and events.
- Keep Apple VF details behind the helper protocol.
- Fail closed on invalid VM config.
- Prefer narrow protocols over shell-string execution.

## Consumer boundary

Consumers supply policy, audit meaning, identity, and user intent. This project
owns kernels, rootfs conversion, VM commands, and state reporting.
