# microagent-kit

`microagent-kit` runs Linux workspaces inside microVMs.

The command-line tool is `microagent`. On macOS, Apple Virtualization.framework
access lives in `microagent-applevf-helper`, a small JSON helper that other
languages can call directly.

Microagent provides the kernel, converts OCI images into VM disks, and starts
the VM. Identity, policy, credentials, and higher-level control stay outside
this project.

## Build

```bash
go test ./...
swift build --package-path helpers/applevf --disable-sandbox
```

Run the smokes:

```bash
make smoke
```

Run the OCI rootfs smoke:

```bash
make smoke-rootfs
```

Build and ad-hoc sign the Apple VF helper:

```bash
make signed-helper
```

Boot a Linux VM:

```bash
make smoke-boot
```

The boot smoke looks for the kernel at `~/.microagent/kernels/apple-vf/arm64/Image`.
The older `~/.microagent/kernels/apple-vf/Image` path still works.

## CLI

Check the host:

```bash
microagent doctor
```

Run a command:

```bash
microagent run \
  --image docker.io/library/ubuntu:24.04 \
  --exec "uname -a"
```

Create a workspace:

```bash
microagent create \
  --name research \
  --image docker.io/library/ubuntu:24.04
```

The image supplies Linux userspace. Microagent creates the disk and starts the
VM.

Create a workspace from an existing rootfs:

```bash
microagent create \
  --id agent-1 \
  --kernel /tmp/kernel \
  --rootfs /tmp/rootfs.ext4 \
  --state-dir /tmp/microagent-kit
```

Validate an existing rootfs:

```bash
microagent create --dry-run \
  --id agent-1 \
  --kernel /tmp/kernel \
  --rootfs /tmp/rootfs.ext4 \
  --state-dir /tmp/microagent-kit
```

Show state:

```bash
microagent status agent-1 --state-dir /tmp/microagent-kit
```

Use JSON:

```bash
microagent create --json request.json
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

The command prints JSON.

Delete state:

```bash
microagent delete agent-1 --state-dir /tmp/microagent-kit
```

Build a rootfs:

```bash
microagent rootfs build \
  --image docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6 \
  --arch arm64 \
  --size-mib 64 \
  --mke2fs /opt/homebrew/opt/e2fsprogs/sbin/mke2fs \
  --out /tmp/busybox-rootfs.ext4
```

Use a local helper build:

```bash
microagent create -helper ./helpers/applevf/.build/debug/microagent-applevf-helper --json request.json
```

## Helper

The helper reads one JSON request from stdin and writes one JSON response to
stdout. See `docs/protocol.md`.

## Boundary

`microagent-kit` handles the VM work:

```text
caller
  -> microagent-kit
       -> Apple Virtualization.framework backend
       -> OCI image to ext4 rootfs builds
```
