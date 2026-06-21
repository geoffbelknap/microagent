---
title: Platform support policy
description: Know which host platforms are supported, compatibility targets, or experimental.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-21_

microagent is a host virtualization project, so platform support depends on
the host hypervisor, kernel devices, networking, block devices, guest
transport, cleanup behavior, and packaging. Linux and macOS are the supported
host targets.

| Status | Host | Backend | What to expect |
|---|---|---|---|
| Supported | Linux | `linux-kvm` / Firecracker | Recommended for regular use. Requires Linux KVM, vsock, TUN, Firecracker, and `pasta` for the default `user` network mode. |
| Supported | macOS on Apple silicon | `apple-vf` / Apple Virtualization.framework | Recommended for regular use on Apple silicon Macs. Uses Apple's Virtualization.framework through the packaged Swift supervisor. |
| Compatibility target | WSL | `linux-kvm` when host prerequisites are available | Intended to work through the Linux backend when WSL exposes the Linux capabilities Firecracker needs. Run `microagent doctor` inside WSL before creating workspaces. |
| Experimental | Windows | `windows-hyperv` / Hyper-V HCS | Available for evaluation on Windows hosts. Behavior and coverage may change; see the [Windows Hyper-V supervisor](/protocol/windows-hyperv/) notes before relying on it. |

## What `doctor` Means

`microagent doctor` reports the active backend and the host capabilities
microagent can see from the current shell. On Linux and WSL, it checks the
Firecracker path, KVM, vsock, TUN, `pasta`, user namespaces, guest init, and
kernel availability. On macOS, it checks the Apple VF supervisor and kernel. On
Windows, it checks the experimental Hyper-V/HCS path.

If `doctor` reports a missing prerequisite, fix that host capability before
starting a workspace.
