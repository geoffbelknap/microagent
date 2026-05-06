---
title: Smoke tests
description: Maintainer checks that boot real VMs.
---

Smoke tests are for maintainers and operators who need to prove backend changes
against real host virtualization. They are not part of the normal user install
path; use [`microagent doctor`](/cli/doctor/) when you only need to check
whether a host is ready to run VMs.

The Makefile drives these checks. `make smoke` selects the right suite for the
host: Firecracker on Linux, Apple VF on macOS.

## Suites

| Target | What it covers |
|---|---|
| `make smoke` | Default per-host smoke suite |
| `make smoke-rootfs` | OCI image to ext4 rootfs conversion |
| `make smoke-firecracker` | Linux KVM Firecracker boot |
| `make smoke-workspace` | HostOS workspace lifecycle |
| `make smoke-boot` | Boot a Linux VM end-to-end (Apple VF) |

## Firecracker Boot

Run from the `microagent-kit` checkout on a Linux amd64 host with KVM:

```bash
make smoke-firecracker
```

This target must run **outside sandboxed agent environments**. It needs host
KVM visibility, network access for OCI layer fetches, and normal writes to
Microagent state paths.

This check:

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

## Apple VF Boot

```bash
make signed-supervisor    # build + ad-hoc sign the supervisor
make smoke-boot
```

This check looks for the kernel at
`~/.microagent/kernels/apple-vf/arm64/Image`. The older
`~/.microagent/kernels/apple-vf/Image` path still works.
