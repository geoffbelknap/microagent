---
title: Host requirements
description: Check what Linux, macOS, and WSL hosts need to boot workspaces.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-25_

microagent runs each workspace as a real Linux microVM, and the host has to
provide the virtualization that VM needs. Linux and macOS are the supported
release targets:

- **Linux** is supported through the Linux KVM backend (Firecracker).
- **macOS on Apple silicon** is supported through Apple
  Virtualization.framework.
- **WSL** is a compatibility lane through the Linux backend when WSL exposes the
  Linux host capabilities microagent needs.

The docs describe the behavior you should expect on supported hosts in the
current release. Start with the command you want to run; use `doctor` when the
host itself is the question.

## Check the host

Run `microagent doctor` on the machine that will boot workspaces:

```bash
microagent doctor
```

`doctor` reports the active backend and checks the host path, supervisor, guest
init, kernel, virtualization, console support, and the networking prerequisites
for the default `user` network mode. If it reports a missing prerequisite, fix
that host capability before starting a workspace.

## Linux

Linux hosts need:

- KVM available to the current user
- vsock support
- `/dev/net/tun`
- unprivileged user namespaces
- `pasta` for the default `user` network mode
- the installed microagent supervisor and guest init
- a default microagent kernel

Source installs try to put the required binaries in the install prefix. If host
policy blocks user namespaces or KVM, fix that policy before starting a
workspace.

## macOS

macOS hosts need:

- Apple silicon
- Apple Virtualization.framework
- the installed microagent Apple VF supervisor
- the installed guest init
- a default arm64 microagent kernel

## WSL

WSL uses the Linux path. It is not a separate product mode. Run
`microagent doctor` inside the WSL environment; it must report the Linux
prerequisites as available.

## Overrides

Most users should let microagent pick the host backend. Use `--backend` only
when testing a specific host path:

```bash
microagent doctor --backend linux-kvm
microagent doctor --backend apple-vf
```

If a request names a backend that does not match the host, microagent fails
before building a rootfs or starting a supervisor.
