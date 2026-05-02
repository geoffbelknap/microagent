# microagent-vmkit

`microagent-vmkit` is a toolkit and CLI for running AI agents inside
inspectable microVMs.

It gives agent runtimes a host-side API for preparing, starting, inspecting,
stopping, and cleaning up microVM-backed workloads. VM policy, model calls,
tool mediation, credentials, and operator UX stay with the caller.

Apple Virtualization.framework on Apple silicon is the first backend. The API
keeps backend details out of the caller's lifecycle contract.

## Goals

- Swift library for microVM lifecycle operations
- CLI for local validation and scripting
- structured lifecycle events
- runtime identity and metadata on every request and event
- vsock-first host/guest control paths
- policy, audit, credentials, and agent semantics left to the caller

## Build

```bash
swift build
swift test
```

## CLI

Check whether the host can use Apple Virtualization.framework:

```bash
microagent-vmkit apple-vf-host-check
```

Validate a lifecycle request:

```bash
microagent-vmkit validate-config request.json
```

Input:

```json
{
  "identity": {
    "requestID": "req-1",
    "runtimeID": "agent-1",
    "role": "workload",
    "backend": "apple-vf"
  },
  "config": {
    "kernelPath": "/tmp/kernel",
    "rootfsPath": "/tmp/rootfs.ext4",
    "stateDir": "/tmp/microagent-vmkit",
    "memoryMiB": 512,
    "cpuCount": 2
  }
}
```

The command prints a JSON lifecycle event if the request is valid.

## Boundary

`microagent-vmkit` handles VM lifecycle work:

```text
agent runtime
  -> microagent-vmkit
       -> Apple Virtualization.framework backend
  -> microvm-rootfs
       -> OCI image to bootable rootfs artifact
```

## Companion Project

Use `microvm-rootfs` to turn OCI images into rootfs artifacts for this project
or other microVM runtimes.
