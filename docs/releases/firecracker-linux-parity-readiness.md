---
title: Firecracker Linux Parity Readiness
description: Linux Firecracker parity validation record for console, publish, lifecycle, and boot gates.
---

This note records the Linux Firecracker parity validation performed for the
branch containing commit `84d5dd9`.

## Host

```text
distro=Ubuntu 24.04.4 LTS
kernel=6.8.0-110-generic
arch=x86_64
dev_kvm=crw-rw---- 1 root kvm 10, 232 /dev/kvm
dev_vhost_vsock=crw-rw---- 1 root kvm 10, 241 /dev/vhost-vsock
firecracker=Firecracker v1.15.1
```

## Commands

The live KVM commands were run outside sandboxed agent environments:

```bash
scripts/linux-host-facts.sh
scripts/go-test.sh
scripts/firecracker-console-parity-smoke.sh
scripts/firecracker-publish-smoke.sh
scripts/firecracker-workspace-smoke.sh
scripts/firecracker-boot-smoke.sh
make smoke
```

## Results

| Gate | Result |
|---|---|
| `scripts/go-test.sh` | pass |
| `scripts/firecracker-console-parity-smoke.sh` | pass |
| `scripts/firecracker-publish-smoke.sh` | pass |
| `scripts/firecracker-workspace-smoke.sh` | pass |
| `scripts/firecracker-boot-smoke.sh` | pass |
| `make smoke` | pass |

The Firecracker boot smoke verified kernel SHA:

```text
4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0
```

## Performance

Performance was captured with a locally built CLI, supervisor, and guest-init
from the validated branch:

| Command | Result |
|---|---|
| `microagent perf boot` | one successful iteration, `18693 ms` |
| `microagent perf footprint perf-steady3` | `67640 KiB` RSS |
| `microagent perf steady perf-steady3 --duration 5 --interval 1` | min/avg/max `67640/67640/67640 KiB` RSS |

## Parity Surface

Validated Firecracker behavior:

- `microagent connect <name> --send "echo CONNECT_READY"` reaches a running
  guest shell.
- interactive `microagent connect <name>` waits for shell readiness by default.
- `Ctrl-]` detaches without stopping the workspace.
- `microagent logs <name>` continues to expose serial output.
- `--publish 127.0.0.1:<hostPort>:8080/tcp` forwards host TCP into a running
  Firecracker workspace.
- lifecycle smokes continue to cover prepare, start, status, stop, delete, and
  running-delete refusal.
