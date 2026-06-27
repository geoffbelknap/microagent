---
title: Security
description: Know what microagent verifies, what it treats as your input, and how to report.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-27_

## Trust boundary

What `microagent` secures is the VM layer: it verifies the kernel against a
known SHA-256, pins the rootfs image by digest, reports runtime verification
hashes you can check before `start`, and runs a host supervisor you can sign.
Everything above the VM boundary belongs to the caller. microagent treats the
kernel, rootfs, and request files as **executable input**. It does not sign
images, scan layers, mediate credentials, or enforce policy. Those concerns
belong to the system that calls `microagent`. See
[Boundaries](/concepts/boundaries/) for the full list.

That means:

- The kernel that boots is whoever installed `~/.microagent/kernels/...`.
  Verify with [`microagent kernel verify`](/cli/kernel/) when this matters.
  In practice, anyone who can write to that directory decides what kernel
  your workspaces boot - protect it like a binary on `PATH`, and verify
  before boots you care about.
- The rootfs is whatever OCI image the caller specified. Pin by digest in
  production - a tag can resolve to different content tomorrow, and only a
  digest pin makes the workspace contents reproducible and attestable.
  `microagent rootfs build` rejects mutable tag references unless you pass
  `--allow-mutable`.
- `microagent --json status <name>` reports verification hashes for the image,
  kernel, rootfs, and injected init. Treat `verification.ok: false` as a stop
  sign until you understand the divergence. Tamper detection is available
  before every `start`, but it only protects you if your automation actually
  checks it - wire the check into any pipeline that boots workspaces
  unattended.
- The host supervisor is whichever binary is on PATH (or pointed to by
  `--supervisor`, `MICROAGENT_APPLEVF_SUPERVISOR`, or
  `MICROAGENT_FIRECRACKER_SUPERVISOR`). Use signed builds in production.
  The supervisor runs with your privileges on the host side of every VM
  boundary - an attacker who can swap that binary owns every workspace, so
  pin its path and verify its provenance.

## Reporting

For the disclosure flow, supported versions, and response expectations, see [`SECURITY.md`](https://github.com/geoffbelknap/microagent/blob/main/SECURITY.md) at the repository root.
