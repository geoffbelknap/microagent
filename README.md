# microagent-vmkit

`microagent-vmkit` is a toolkit and CLI for running AI agents inside
inspectable microVMs.

It is not a general-purpose Mac VM manager and it is not an agent framework.
The project sits between those layers: it gives agent runtimes a small,
structured host-side API for preparing, starting, inspecting, stopping, and
cleaning up microVM-backed agent workloads.

Apple Virtualization.framework on Apple silicon is the first backend. The API
is intentionally shaped so other microVM backends can follow without changing
consumer code.

## Initial Goals

- Provide a Swift library for microVM lifecycle operations.
- Provide a `microagent-vmkit` CLI for local validation and scripting.
- Emit structured lifecycle events.
- Preserve runtime identity and metadata in every request and event.
- Support vsock-first host/guest control paths.
- Keep policy, audit, credentials, and agent semantics outside this repo.

## Relationship To Agency

Agency is the first reference consumer. Agency owns ASK enforcement, audit,
credentials, enforcer topology, runtime manifests, and operator UX.

`microagent-vmkit` owns only the generic VM lifecycle substrate:

```text
agency
  -> microagent-vmkit
       -> Apple Virtualization.framework backend
  -> microvm-rootfs
       -> OCI image to bootable rootfs artifact
```

## Companion Project

Use `microvm-rootfs` to turn OCI images into bootable rootfs artifacts for this
project or other microVM runtimes.

## Status

Private bootstrap repo. APIs are not stable.
