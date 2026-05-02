# Boundaries

`microagent-kit` is the VM lifecycle layer for agent runtimes.

## Owned Here

- Host-side VM lifecycle commands
- Structured runtime identity in lifecycle requests
- Structured lifecycle events
- Apple Virtualization.framework helper protocol
- Runtime state files and cleanup semantics
- Host/guest transport primitives such as vsock listeners

## Owned By Consumers

- Agent planning and execution loops
- LLM/provider calls
- Tool mediation
- Policy decisions
- Audit meaning and retention
- Credentials and grants
- User/operator experience

## Owned By `microvm-rootfs`

- OCI image resolution
- OCI layer unpack
- Rootfs filesystem construction
- Guest init injection
- Rootfs provenance

The boundary should let a consumer provide a prepared kernel, rootfs, runtime
identity, and bridge targets. `microagent-kit` should then run lifecycle
commands and report state without learning the consumer's policy model.
