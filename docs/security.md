---
title: Security
description: Trust boundary and reporting issues.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-11_

## Trust boundary

`microagent` treats the kernel, rootfs, and request files as **executable
input**. It does not sign images, scan layers, mediate credentials, or enforce
policy — those concerns belong to the upstream system that calls `microagent`.
See [Boundaries](/concepts/boundaries/) for the full list.

That means:

- The kernel that boots is whoever installed `~/.microagent/kernels/...`.
  Verify with [`microagent kernel verify`](/cli/kernel/) when this matters.
- The rootfs is whatever OCI image the caller specified. Pin by digest in
  production. `microagent rootfs build` rejects mutable tag references
  unless you pass `--allow-mutable`.
- `microagent --json status <name>` reports verification hashes for the image,
  kernel, rootfs, and injected init. Treat `verification.ok: false` as a stop
  sign until you understand the divergence.
- The backend supervisor is whichever binary is on PATH (or pointed to by
  `--supervisor`, `MICROAGENT_APPLEVF_SUPERVISOR`, or
  `MICROAGENT_FIRECRACKER_SUPERVISOR`). Use signed builds in production.

## Reporting

For the disclosure flow, supported versions, and response expectations, see [`SECURITY.md`](https://github.com/geoffbelknap/microagent/blob/main/SECURITY.md) at the repository root.
