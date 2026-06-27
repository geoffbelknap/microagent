---
title: Host requirements
description: Check what Linux, macOS, WSL, and experimental Windows hosts need.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-27_

microagent supports Linux and macOS hosts. WSL can work when it exposes the
Linux virtualization features microagent needs. Windows Hyper-V is
experimental.

Run `microagent doctor` on the machine that will boot workspaces:

```bash
microagent doctor
```

`doctor` checks the host path, supervisor, guest init, kernel, virtualization,
console support, and the networking prerequisites for the default `user`
network mode.

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

WSL uses the Linux path. It is not a separate product mode, and microagent does
not fall back from WSL to Windows Hyper-V. Run `microagent doctor` inside the
WSL environment; it must report the Linux prerequisites as available.

## Windows Hyper-V

Windows Hyper-V support is experimental. It is useful for evaluation on Windows
hosts, but Linux and macOS are the supported release targets.

## Overrides

Most users should let microagent pick the host backend. Use `--backend` only
when testing a specific host path:

```bash
microagent doctor --backend linux-kvm
microagent doctor --backend apple-vf
```

If a request names a backend that does not match the host, microagent fails
before building a rootfs or starting a supervisor.
