# microagent-kit

`microagent-kit` runs Linux workspaces inside microVMs.

The command-line tool is `microagent`. On macOS it uses Apple
Virtualization.framework through `microagent-applevf-helper`, a small JSON
helper that Go, Python, Rust, Node, and shell scripts can call.

Microagent provides the kernel, converts OCI images into VM disks, and starts
the VM. Identity, policy, credentials, and higher-level control stay outside
this project.

## Install

```bash
brew install geoffbelknap/tap/microagent-kit
```

Then run a command from an OCI image:

```bash
microagent run \
  --image docker.io/library/ubuntu:24.04 \
  --exec "uname -a"
```

Microagent downloads its default kernel the first time it needs one.

Or create a named workspace:

```bash
microagent create \
  --name research \
  --image docker.io/library/ubuntu:24.04 \
  --size-mib 2048 \
  --memory 1024 \
  --cpus 2 \
  --setup "mkdir -p /workspace" \
  --setup "echo ready > /workspace/status"
```

Start it and run a command through the console:

```bash
microagent start research
microagent connect research --send "cat /etc/os-release; cat /workspace/status; uname -m"
```

Check state, read the boot log, and remove the workspace:

```bash
microagent ps
microagent status --name research
microagent logs research
microagent stop research
microagent delete research
```

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

Run the Linux Firecracker boot smoke outside sandboxed environments so KVM,
network, and Microagent state paths are visible:

```bash
make smoke-firecracker
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

The output includes host support and default kernel status.
On Linux, run `microagent` outside sandboxed agent environments when checking
KVM or booting Firecracker microVMs.

Run a command from an image:

```bash
microagent run \
  --image docker.io/library/ubuntu:24.04 \
  --exec "uname -a"
```

Run setup commands first:

```bash
microagent run \
  --image docker.io/library/busybox:1.36 \
  --setup "mkdir -p /workspace" \
  --setup "echo ready > /workspace/status" \
  --exec "cat /workspace/status"
```

Create a workspace:

```bash
microagent create \
  --name research \
  --image docker.io/library/ubuntu:24.04
```

The image supplies Linux userspace. Microagent creates the disk and records the
workspace. If the default kernel is missing, Microagent installs it first.

Prepare a workspace before saving it:

```bash
microagent create \
  --name research \
  --image docker.io/library/busybox:1.36 \
  --setup "mkdir -p /workspace" \
  --setup "echo ready > /workspace/status"
```

Start a workspace:

```bash
microagent start research
```

Open its console:

```bash
microagent connect research
```

For scripts, send one line and print any new console output:

```bash
microagent connect research --send "cat /workspace/status"
```

List workspaces:

```bash
microagent ps
```

Show workspace state:

```bash
microagent status --name research
```

Show boot logs:

```bash
microagent logs research
```

Stop and remove a workspace:

```bash
microagent stop research
microagent delete research
```

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

Use request JSON:

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

Commands print JSON.

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

Use a custom kernel when you need one:

```bash
microagent run \
  --image docker.io/library/ubuntu:24.04 \
  --exec "uname -a" \
  --kernel /tmp/Image
```

Use a local helper build:

```bash
microagent create -helper ./helpers/applevf/.build/debug/microagent-applevf-helper --json request.json
```

## Helper

The helper reads one JSON request from stdin and writes one JSON response to
stdout. See `docs/protocol.md`.

## Boundary

`microagent-kit` stays at the VM boundary:

```text
your program
  -> microagent-kit
       -> Apple Virtualization.framework backend
       -> OCI image to ext4 rootfs builds
```
