---
title: Security
description: What microagent enforces at the VM boundary, where each control is documented, and how to report a vulnerability.
---

<!-- docs-last-updated -->
_Last updated: 2026-09-02_

microagent secures the VM layer. Each workspace is a microVM with its own
kernel, disk, and network device, and the host mediates what crosses that
boundary. This page names each control, says in a sentence or two what it
does, and links to the page that explains it in full. The
[trust boundary](#trust-boundary) section states what microagent verifies
and what it treats as your input.

## What microagent enforces

**Isolation.** Every workspace boots its own Linux kernel from its own disk.
There are no host directory bind mounts, no privileged mode, and no shared
kernel. See [the VM boundary](concepts/architecture.md#the-vm-boundary) and
[limitations](concepts/limitations.md).

**Egress mediation.** By default a workspace can reach the public internet
and nothing else. LAN, host, link-local metadata, and loopback destinations are
denied, and the host records every decision, allowed or not.
`--egress-lock-allowlist` confines a workspace to the destinations you name,
and the mediator becomes its only DNS resolver, so an unlisted name never
resolves. TLS is spliced without interception unless you opt into `mitm`. See
[egress mediation](concepts/egress-mediation.md) for the modes and
[confine egress to an allowlist](guides/egress-allowlist.md) for the
walkthrough.

**Egress receipts.** [`microagent egress`](cli/egress.md) shows the mediator's
decisions for a workspace. [`microagent dispatch`](cli/dispatch.md) runs one
task and returns that audit with the result. The record is written on the
host, outside the guest's reach.

**Credentials the guest never holds.** A broker endpoint or credential swap
attaches the real credential on the host. The agent sends a placeholder
request, and the secret is injected before the request leaves for the
upstream. A [semantic broker grant](guides/broker-grants.md) also limits the
call to declared operations, reauthorizes every redirect, and scans the
response for the injected value. See
[credential swap](concepts/egress-mediation.md#credential-swap) and
[where the swap happens](concepts/architecture.md#where-the-credential-swap-happens).

**Secrets without disk.** When the workload must read a credential itself,
[`microagent secret`](cli/secret.md) places it on a tmpfs at `/run/secrets`
with mode 0400, never on the rootfs or any disk. On-demand secrets are
fetched per request over a socket and never written to a file.
`--secrets-audit` records every access on the host, without the value.
Ordinary snapshots purge the tmpfs before capturing memory and restore it
after resume. See [deliver secrets](guides/secrets.md).

**Risky combinations need an acknowledgment.** A routable workspace cannot
combine guest-delivered secrets, injected files or disks, and `--egress off`
unless you record a reason with `--acknowledge-capability-risk`. The create result
and the manifest report the derived capability categories. See
[the egress modes](concepts/egress-mediation.md#the-egress-modes).

**Bounded by default.** Every mediated workspace carries a per-flow rate cap,
a total-bytes cap, and a concurrent-connection cap. A persistent workspace's
lifetime lease defaults to seven days, and the host caps how many workspaces
run at once. `status` reports every bound in force under `boundedOperations`.
See [bounded operations](concepts/egress-mediation.md#bounded-operations).

**Operator override the guest cannot reach.** [`halt`](cli/halt.md),
[`kill`](cli/kill.md), [`pause`](cli/pause.md), and
[`quarantine`](cli/quarantine.md) are host commands the guest cannot reach.
`halt` gives the guest a bounded window to shut down cleanly; `kill` and
`quarantine` do not wait for it. `quarantine` writes a durable containment
marker, freezes the guest, severs every host-side authority path, captures
evidence while frozen, and stops the VM into custody. `kill` and `quarantine`
require an audit reason.

**Audit written by the host.** [`microagent events`](cli/events.md) returns
the workspace trajectory: lifecycle transitions joined with egress, broker,
constraint, and secret-access records. The host writes every stream, and the
egress, broker, and secret-access logs are append-only. `quarantine` adds an incident receipt that summarizes the session
without copying secret values or request content.

**Boot artifacts you can verify.** The kernel is checked against a known
SHA-256, the rootfs is pinned by image digest, and
`microagent --json status` reports verification hashes for the kernel,
rootfs, injected init, and config disk. Tamper detection runs before every
start. The
[trust boundary](#trust-boundary) below says what that does and does not
cover.

**Guest-to-host calls on a declared channel.** The
[mediation channel](guides/agents-and-mediation.md) is the one path from the
agent to your host control plane. It is declared up front and fails closed
unless you opt out. Your listener decides what each call may do.

## Conformance

[`ASK-CONFORMANCE.md`](https://github.com/geoffbelknap/microagent/blob/main/ASK-CONFORMANCE.md)
records where microagent stands against each invariant of the
[ASK framework](https://askframework.org), with live evidence where an
invariant could be tested. It is a scope declaration, not a certification.
microagent is a substrate, so several invariants are delegated to the layer
that builds on it, and a delegated verdict is not a pass.

## Trust boundary

What `microagent` secures is the VM layer. It does four things:

- Verifies the kernel against a known SHA-256.
- Pins the rootfs image by digest.
- Reports runtime verification hashes you can check before `start`.
- Runs a host supervisor you can sign.

Everything above the VM boundary belongs to the caller. microagent treats the
kernel, rootfs, and request files as **executable input**.

It does not sign images or scan layers. It does not decide who may use a
credential. It does not assign policy or audit meaning.

It does resolve operator-declared secret references, deliver secrets to a
guest, and swap a host-held credential into an outbound request.

The caller owns the rest: authorization, credential eligibility, grants,
retention policy, and reading audit records. See
[Boundaries](concepts/boundaries.md) for the full list.

That means:

- The kernel that boots is whoever installed `~/.microagent/kernels/...`.
  Verify with [`microagent kernel verify`](cli/kernel.md) when this matters.
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
  sign until you understand the divergence. Tamper detection runs before
  every `start`, but it only protects you if your automation checks it -
  wire the check into any pipeline that boots workspaces unattended.
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
Treat them as secret-bearing evidence:

- Keep them outside workload-readable paths.
- Restrict operator access.
- Protect backups and copies.
- Delete them under your evidence-retention process. They are marked retained and cannot be
restored. See [forensic captures](cli/snapshot.md#forensic-captures).

## Reporting

For the disclosure flow, supported versions, and response expectations, see [`SECURITY.md`](https://github.com/geoffbelknap/microagent/blob/main/SECURITY.md) at the repository root.
