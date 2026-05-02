# microagent-kit

`microagent-kit` is a Go library, CLI, and Swift helper for running AI agents
inside inspectable Apple Virtualization.framework microVMs.

The command-line tool is `microagent`. Apple VF access lives in
`microagent-applevf-helper`, a standalone process with a JSON stdin/stdout
protocol. Go, Python, Rust, Node, and shell scripts can call the helper without
linking Swift.

VM policy, model calls, tool mediation, credentials, and operator UX stay with
the caller.

## Build

```bash
go test ./...
swift build --package-path helpers/applevf
```

## CLI

Check whether the host can use Apple Virtualization.framework:

```bash
microagent host
```

Validate a lifecycle request without writing state:

```bash
microagent check request.json
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
    "stateDir": "/tmp/microagent-kit",
    "memoryMiB": 512,
    "cpuCount": 2
  }
}
```

The command prints a JSON response if the request is valid.

Prepare, inspect, and delete state:

```bash
microagent prepare request.json
microagent inspect request.json
microagent delete request.json
```

These commands use `config.stateDir` from the request as the state root. The
`start` command exists, but VM launch is not wired yet.

Set `MICROAGENT_APPLEVF_HELPER` or pass `-helper` to point the Go CLI at a
specific helper binary:

```bash
microagent prepare -helper ./helpers/applevf/.build/debug/microagent-applevf-helper request.json
```

## Helper Protocol

The helper accepts one JSON request on stdin and writes one JSON response to
stdout. The protocol is documented in `docs/protocol.md`.

## Boundary

`microagent-kit` handles VM lifecycle work:

```text
agent runtime
  -> microagent-kit
       -> Apple Virtualization.framework backend
  -> microvm-rootfs
       -> OCI image to bootable rootfs artifact
```

## Companion Project

Use `microvm-rootfs` to turn OCI images into rootfs artifacts for this project
or other microVM runtimes.
