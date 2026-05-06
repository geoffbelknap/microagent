---
title: Security
description: Trust boundary and reporting issues.
---

## Trust boundary

`microagent-kit` treats the kernel, rootfs, and request files as **executable
input**. Microagent does not sign images, scan layers, mediate credentials,
or enforce policy — those concerns belong to the upstream system that calls
`microagent`. See [Boundaries](/concepts/boundaries/) for the full list.

In practice that means:

- The kernel that boots is whoever installed `~/.microagent/kernels/...`.
  Verify with [`microagent kernel verify`](/cli/kernel/) when this matters.
- The rootfs is whatever OCI image the caller specified. Pin by digest in
  production. `microagent rootfs build` rejects mutable tag references
  unless you pass `--allow-mutable`.
- The backend supervisor is whichever binary is on PATH (or pointed to by
  `--supervisor`, `MICROAGENT_APPLEVF_SUPERVISOR`, or
  `MICROAGENT_FIRECRACKER_SUPERVISOR`). Use signed builds in production.

## Reporting

Report security issues privately via GitHub's "Report a vulnerability" flow
on the [microagent-kit repo](https://github.com/geoffbelknap/microagent-kit/security).
Do not file public issues for security problems.
