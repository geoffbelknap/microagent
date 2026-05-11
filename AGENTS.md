# microagent

Go CLI/library plus backend supervisors for running Linux workspaces in
microVMs.

## Scope

This repository owns the VM pieces:

- create, start, status, halt, quarantine, stop, kill, and delete commands
- rootfs builds from OCI images
- guest metadata and identity propagation
- serial console, block-device, network, and vsock wiring
- readiness, structured results, declared artifacts, and event history
- cleanup and state files
- Firecracker supervisor
- Apple Virtualization.framework supervisor protocol

## Non-goals

- Do not implement orchestration, planning, LLM calls, tools, or memory.
- Do not implement policy, audit meaning, credential mediation, or enforcement
  decisions. Other projects own those.
- Do not become a general-purpose Mac VM manager. Lima, Tart, vfkit, and Lume
  already serve that space.
- Do not grow rootfs build logic into a general image scanner, signer, or
  registry management tool.

## Design rules

- Keep public output structured and machine-readable.
- Keep the Apple VF supervisor usable from Go, Python, Rust, Node, and shell scripts.
- Treat state changes as API output, not log strings.
- Keep halt, quarantine, readiness, result, artifact, and verification semantics backend-neutral.
- Preserve explicit identity in requests, state files, and events.
- Keep backend details behind supervisor boundaries.
- Fail closed on invalid VM config.
- Prefer narrow protocols over shell-string execution.

## Project boundary

Other projects supply policy, audit meaning, identity, and user intent. This
project owns kernels, rootfs conversion, VM commands, runtime verification, and
state reporting.
