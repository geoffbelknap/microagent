---
title: Firecracker amd64 Boot-Proven Release
description: First microagent-kit release proven to boot a Firecracker amd64 guest from the default kernel.
---

`microagent-kit v0.1.22` is the first release proven to boot a Firecracker
amd64 Microagent guest from the default released kernel path.

Kernel artifact:

- release: `microagent-kernels` `kernels-6.1.155-r2`
- asset: `microagent-kernel-6.1.155-firecracker-amd64`
- SHA-256: `4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0`

Validated on Linux amd64 with:

```sh
microagent doctor
microagent kernel install
make smoke-firecracker
```

Expected smoke output:

```text
firecracker boot smoke passed
kernel_sha=4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0
```

The Firecracker smoke target lives in `microagent-kit` and must run outside
sandboxed agent environments so KVM, network, and Microagent state paths are
visible.
