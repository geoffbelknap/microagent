# Boundaries

`microagent-kit` is the VM lifecycle layer for agent runtimes.

## In this repo

- Host-side VM lifecycle commands
- OCI image to ext4 rootfs builds
- Structured runtime identity in lifecycle requests
- Structured lifecycle events
- Apple Virtualization.framework helper protocol
- Runtime state files and cleanup semantics
- Host/guest transport primitives such as vsock listeners

## Left to callers

- Agent planning and execution loops
- LLM/provider calls
- Tool mediation
- Policy decisions
- Audit meaning and retention
- Credentials and grants
- User/operator experience

Callers provide the kernel, rootfs, runtime identity, and bridge targets.
`microagent-kit` runs lifecycle commands and reports state without taking over
the caller's policy model.
