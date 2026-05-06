---
title: Smoke tests
description: Lifecycle and boot smokes for both backends.
---

The Makefile drives all smoke suites. `make smoke` selects the right one for
the host: Firecracker on Linux, Apple VF on macOS.

The normal GitHub CI workflow keeps unit tests separate from live KVM gates.
Live Firecracker parity runs through the manual `Live Linux Parity` workflow on
self-hosted Linux runners labeled `kvm`, or directly on a Linux host with
`scripts/linux-parity-handoff-strict.sh`.

## Suites

| Target | What it covers |
|---|---|
| `make smoke` | Default per-host smoke suite |
| `make smoke-rootfs` | OCI image to ext4 rootfs conversion |
| `make smoke-firecracker` | Linux KVM Firecracker boot |
| `make smoke-firecracker-console` | Linux KVM Firecracker console parity |
| `make smoke-firecracker-publish` | Linux KVM Firecracker TCP publish |
| `make smoke-workspace` | HostOS workspace lifecycle |
| `make smoke-boot` | Boot a Linux VM end-to-end (Apple VF) |

## Firecracker boot smoke

Run from the `microagent-kit` checkout on a Linux amd64 host with KVM:

```bash
make smoke-firecracker
```

This target must run **outside sandboxed agent environments**. It needs host
KVM visibility, network access for OCI layer fetches, and normal writes to
Microagent state paths.

The smoke:

- builds local `microagent` and `microagent-guestinit` binaries
- installs the default Firecracker amd64 kernel
- verifies the kernel SHA
- builds a BusyBox OCI-backed rootfs
- boots with Firecracker
- runs `echo microagent-firecracker-boot-smoke` in the guest
- verifies Firecracker exits cleanly

Expected kernel SHA:

```text
4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0
```

Expected output:

```text
firecracker boot smoke passed
kernel_sha=4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0
```

`microagent-kernels` owns kernel build and release artifacts. Its matching
non-KVM check is:

```bash
make check-kernel-config-amd64
```

## Firecracker console parity smoke

```bash
make smoke-firecracker-console
```

This Linux amd64 KVM target verifies that `microagent connect` reaches a
running Firecracker guest shell, `Ctrl-]` detaches without stopping the
workspace, and serial output remains available through `microagent logs`.

## Firecracker publish smoke

```bash
make smoke-firecracker-publish
```

This Linux amd64 KVM target verifies that `--publish` forwards a host TCP
listener into a running Firecracker workspace. It checks both a minimal TCP
responder and a BusyBox HTTP server.

## Apple VF boot smoke

```bash
make signed-supervisor    # build + ad-hoc sign the supervisor
make smoke-boot
```

The smoke looks for the kernel at
`~/.microagent/kernels/apple-vf/arm64/Image`. The older
`~/.microagent/kernels/apple-vf/Image` path still works.
