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
swift build --package-path helpers/applevf --disable-sandbox
```

Run the lifecycle smokes:

```bash
make smoke
```

Run the real OCI rootfs smoke when `mke2fs` is installed and network access is
available:

```bash
make smoke-rootfs
```

## CLI

Check whether the host can use Apple Virtualization.framework:

```bash
microagent doctor
```

Create runtime state:

```bash
microagent create \
  --id agent-1 \
  --kernel /tmp/kernel \
  --rootfs /tmp/rootfs.ext4 \
  --state-dir /tmp/microagent-kit
```

Validate without writing state:

```bash
microagent create --dry-run \
  --id agent-1 \
  --kernel /tmp/kernel \
  --rootfs /tmp/rootfs.ext4 \
  --state-dir /tmp/microagent-kit
```

Read state:

```bash
microagent status agent-1 --state-dir /tmp/microagent-kit
```

JSON is still available for automation:

```bash
microagent create --json request.json
microagent create --json - < request.json
```

Request JSON:

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

Delete state:

```bash
microagent delete agent-1 --state-dir /tmp/microagent-kit
```

The `start` command exists, but VM launch is not wired yet.

Build a rootfs from an OCI image:

```bash
microagent rootfs build \
  --image docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6 \
  --arch arm64 \
  --size-mib 64 \
  --mke2fs "$(brew --prefix e2fsprogs)/sbin/mke2fs" \
  --out /tmp/busybox-rootfs.ext4
```

Set `MICROAGENT_APPLEVF_HELPER` or pass `-helper` to point the Go CLI at a
specific helper binary:

```bash
microagent create -helper ./helpers/applevf/.build/debug/microagent-applevf-helper --json request.json
```

## Helper protocol

The helper accepts one JSON request on stdin and writes one JSON response to
stdout. The protocol is documented in `docs/protocol.md`.

## Boundary

`microagent-kit` handles VM lifecycle work:

```text
agent runtime
  -> microagent-kit
       -> Apple Virtualization.framework backend
       -> OCI image to ext4 rootfs builds
```
