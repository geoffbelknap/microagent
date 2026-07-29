---
title: Security
description: Know what microagent verifies, what it treats as your input, and how to report.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-29_

## Trust boundary

What `microagent` secures is the VM layer. It verifies the kernel against a
known SHA-256, pins the rootfs image by digest, reports runtime verification
hashes you can check before `start`, and runs a host supervisor you can sign.
Everything above the VM boundary belongs to the caller. microagent treats the
kernel, rootfs, and request files as **executable input**. It does not sign
images, scan layers, decide who may use a credential, or assign policy and
audit meaning. It can resolve operator-declared secret references, deliver
secrets to a guest, and mechanically substitute a host-held credential into an
outbound request. The caller remains responsible for authorization, credential
eligibility, grants, retention policy, and interpreting audit records. See
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
  kernel, rootfs, injected init, and the per-boot config disk (the command
and files the guest will run). Treat `verification.ok: false` as a stop
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

## Secret-bearing evidence

Ordinary snapshots purge microagent-managed secret files before capturing
memory and rehydrate them after restore. That guarantee does not cover values a
workload copied into its own memory.

Forensic snapshots deliberately preserve guest memory without secret purging.
Treat them as secret-bearing evidence: keep them outside workload-readable
paths, restrict operator access, protect backups and copies, and delete them
under your evidence-retention process. They are marked retained and cannot be
restored. See [forensic captures](/cli/snapshot/#forensic-captures).

## Reporting

For the disclosure flow, supported versions, and response expectations, see [`SECURITY.md`](https://github.com/geoffbelknap/microagent/blob/main/SECURITY.md) at the repository root.
