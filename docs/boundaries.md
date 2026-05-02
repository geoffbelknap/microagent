# Boundaries

`microagent-kit` runs Linux workspaces inside microVMs.

## In this repo

- VM commands
- OCI image to rootfs builds
- Identity in requests and state files
- State changes as JSON
- Apple Virtualization.framework helper protocol
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

Your program provides identity and bridge targets. Microagent provides the
kernel, rootfs conversion, VM state, and VM commands without taking over policy.
