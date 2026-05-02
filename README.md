# microagent-kit

`microagent-kit` runs Linux workspaces inside microVMs.

The command-line tool is `microagent`. Each host OS has one VM backend:
Firecracker on Linux and Apple Virtualization.framework on macOS. Each backend
has a supervisor that owns VM lifecycle state changes. The Apple VF supervisor
is packaged as `microagent-applevf-supervisor`, a small JSON executable that Go,
Python, Rust, Node, and shell scripts can call.

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

Linux has one backend: Firecracker. macOS has one backend: Apple VF. The
`--backend` flag exists for lower-level request compatibility and backend
smoke tests, not as a user-facing backend selector.

Both backends expose the same lifecycle surface: `run`, `create`, `start`,
`status`, `stop`, `kill`, and `delete`. Backend supervisors record state files
and emit lifecycle events. Firecracker records process IDs so `stop` can send a
graceful signal and `kill` can send a hard kill. Firecracker does not support
interactive `connect`; use `logs` for serial output.

## Build

```bash
go test ./...
swift build --package-path supervisors/applevf --disable-sandbox
```

Run the smokes:

```bash
make smoke
```

`make smoke` runs the feature smoke suite for the HostOS backend: Firecracker
on Linux, Apple VF on macOS.

Run the OCI rootfs smoke:

```bash
make smoke-rootfs
```

Run the Linux Firecracker boot smoke outside sandboxed environments so KVM,
network, and Microagent state paths are visible:

```bash
make smoke-firecracker
```

Run only the HostOS workspace lifecycle smoke:

```bash
make smoke-workspace
```

See [docs/firecracker-smoke.md](docs/firecracker-smoke.md) for the expected
kernel SHA, output, and host requirements.
The boot-proven release note is in
[docs/releases/firecracker-amd64-boot-proven.md](docs/releases/firecracker-amd64-boot-proven.md).

Build and ad-hoc sign the Apple VF supervisor:

```bash
make signed-supervisor
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

Attach an existing ext4 disk:

```bash
microagent create \
  --name research \
  --image docker.io/library/ubuntu:24.04 \
  --disk workspace=/tmp/workspace.ext4:/workspace:rw
```

Build a disk from a tar bundle and attach it read-only:

```bash
microagent create \
  --name research \
  --image docker.io/library/ubuntu:24.04 \
  --bundle config=/tmp/config.tar:/config:ro
```

Create a workspace:

```bash
microagent create \
  --name research \
  --image docker.io/library/ubuntu:24.04
```

The workspace name can also be positional:

```bash
microagent create research --image docker.io/library/ubuntu:24.04
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

`connect` is supported by Apple VF. Firecracker workspaces currently expose
serial output through `logs`.

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

For Firecracker, `delete` refuses to remove state while the recorded VM process
is still running. Use `stop` or `kill` first.

Prepare a workspace from an existing rootfs with the lower-level request form:

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

Use `--json` when scripts need structured output.

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

Use a local Apple VF supervisor build:

```bash
microagent create \
  --backend apple-vf \
  --supervisor ./supervisors/applevf/.build/debug/microagent-applevf-supervisor \
  --json request.json
```

## Supervisors

Firecracker and Apple VF both use the supervisor concept for backend lifecycle
work. The Apple VF supervisor is packaged as a Swift executable because the
Virtualization.framework boundary is host-native. It reads one JSON request from
stdin and writes one JSON response to stdout. See `docs/protocol.md`.

## Boundary

`microagent-kit` stays at the VM boundary:

```text
your program
  -> microagent-kit
       -> Firecracker backend
       -> Apple Virtualization.framework backend
       -> OCI image to ext4 rootfs builds
```
