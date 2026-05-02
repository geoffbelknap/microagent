# Firecracker Boot Smoke

Run this from the `microagent-kit` checkout on a Linux amd64 host with KVM:

```sh
make smoke-firecracker
```

This target must run outside sandboxed agent environments. It needs host KVM
visibility, network access for direct OCI layer fetches, and normal writes to
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

```sh
make check-kernel-config-amd64
```
