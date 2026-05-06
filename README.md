# microagent-kit

`microagent-kit` provides the tools needed to run AI agent workspaces in microVMs.

The command-line tool is `microagent`. Each host OS has one VM backend:
Firecracker on Linux and Apple Virtualization.framework on macOS. Each backend
has a supervisor that owns VM lifecycle state changes. The Apple VF supervisor
is packaged as `microagent-applevf-supervisor`; the Firecracker supervisor is
packaged as `microagent-firecracker-supervisor`. Both are small JSON
executables that Go, Python, Rust, Node, and shell scripts can call.

Microagent provides the kernel, converts OCI images into VM disks, and starts
the VM. Identity, policy, credentials, and higher-level control stay outside
this project.

See [`docs/`](docs/) for the full guide and CLI reference.

Go callers can use `pkg/rootfs` for OCI-to-ext4 builds and `pkg/vmkit` for the
shared supervisor request/response types. The high-level workspace lifecycle API
is still implemented by the CLI. See [`docs/library/go.md`](docs/library/go.md).

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
  --profile medium \
  --setup "mkdir -p /workspace" \
  --setup "echo ready > /workspace/status"
```

Or put the workspace in source control:

```yaml
# microagent.yaml
name: research
image: docker.io/library/ubuntu:24.04
profile: medium
restart: on-failure
setup:
  - mkdir -p /workspace
  - echo ready > /workspace/status
```

```bash
microagent create
```

Start it and run a command through the console:

```bash
microagent start research
microagent connect research --send "cat /etc/os-release; cat /workspace/status; uname -m"
```

Clone a stopped workspace:

```bash
microagent clone research research-copy
```

Copy a file into a stopped workspace:

```bash
microagent cp ./config.json research:/etc/microagent/config.json
```

Check state, read the boot log, and remove the workspace:

```bash
microagent host
microagent ps
microagent profiles
microagent status --name research
microagent logs research
microagent stop research
microagent delete research
```

Linux has one backend: Firecracker. macOS has one backend: Apple VF. The
`--backend` flag exists for lower-level request compatibility and backend
validation, not as a user-facing backend selector.

Both backends expose the same lifecycle surface: `run`, `create`, `start`,
`status`, `halt`, `stop`, `kill`, and `delete`. Backend supervisors record
state files and append lifecycle events to `events.json`. `halt` is the clean,
disk-preserving shutdown path; `stop` remains the graceful stop command and
`kill` sends a hard kill. Both backends support interactive `connect`; `logs`
remains available for captured serial output.

Named workspace manifests also record runtime verification metadata: image
digest, kernel hash, rootfs hash, and injected init hash. `microagent status
--json` recomputes current hashes and reports structured divergence.
Status JSON also reports `guestReady`, `shellReady`, and `resultReady` so
callers can sequence work without polling serial logs or guessing from files.

## Build

```bash
go test ./...
go build ./cmd/microagent
go build ./cmd/microagent-firecracker-supervisor              # Linux only
swift build --package-path supervisors/applevf --disable-sandbox  # macOS only
```

Run the smoke suite for your host backend:

```bash
make smoke
```

`make smoke` runs Firecracker checks on Linux and Apple VF checks on macOS.
These boot real VMs, so run them on a host with the right virtualization access.

Run targeted smokes when you are working in one area:

```bash
make smoke-rootfs
make smoke-firecracker
make smoke-firecracker-console
make smoke-firecracker-publish
make smoke-firecracker-network
make smoke-applevf-network
make smoke-applevf-publish
make smoke-workspace
make signed-supervisor
make smoke-boot
```

See [docs/operations/smoke-tests.md](docs/operations/smoke-tests.md) for host
requirements, expected kernel SHAs, and per-backend smoke targets.

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

`connect` is supported by Apple VF and Firecracker. Press `Ctrl-]` to detach
from an interactive console without stopping the workspace.

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
microagent halt research
microagent delete research
```

For Firecracker, `delete` refuses to remove state while the recorded VM process
is still running. Use `halt`, `stop`, or `kill` first.

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

Firecracker and Apple VF both use executable supervisors for backend lifecycle
work. Linux uses `microagent-firecracker-supervisor`; macOS uses the Swift
`microagent-applevf-supervisor` because the Virtualization.framework boundary
is host-native. Supervisors read one JSON request from stdin and write one JSON
response to stdout. See [docs/protocol/index.md](docs/protocol/index.md),
[docs/protocol/firecracker.md](docs/protocol/firecracker.md), and
[docs/protocol/applevf.md](docs/protocol/applevf.md).

## Boundary

`microagent-kit` stays at the VM boundary:

```text
your program
  -> microagent-kit
       -> Firecracker backend
       -> Apple Virtualization.framework backend
       -> OCI image to ext4 rootfs builds
```
