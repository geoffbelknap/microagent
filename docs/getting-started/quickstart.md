---
title: Quickstart
description: Boot a Linux microVM from an OCI image and run a command inside it.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-27_

Boot a Linux microVM from an OCI image, run one command inside it, and tear it
down. You only need three commands.

## 1. Install

```bash
brew install geoffbelknap/tap/microagent
```

Building from source is covered in [Install](/getting-started/install/).

## 2. Check the host

```bash
microagent doctor
```

`doctor` checks whether this host can boot workspaces and whether the default
kernel is in place. If something is missing, it tells you what to fix. Still
stuck? See [Troubleshooting](/troubleshooting/).

## 3. Boot, run, tear down

```bash
microagent run docker.io/library/ubuntu:24.04 uname -a
```

The first argument is the OCI image. Everything after it is the command to run
inside the microVM. The first run also downloads the default kernel for this
host; later runs reuse it.

```text
Workspace: run-1781164526178302845
State: stopped
Rootfs: /home/agency/.microagent/workspaces/run-1781164526178302845/rootfs.ext4
Profile: small
Restart: never
Network: user
Hostname: run-1781164526178302845
Resources: memory=512MiB cpus=2 disk=1024MiB
Kernel: /home/agency/.microagent/kernels/linux-kvm/amd64/Image
Exit code: 0

Linux run-1781164526178302845 6.1.155 #2 SMP PREEMPT_DYNAMIC Sat May  2 18:32:03 UTC 2026 x86_64 x86_64 x86_64 GNU/Linux
```

If you run an image without a command, microagent uses the image's
Entrypoint/Cmd.

## What just happened

microagent pulled the Ubuntu image, converted it into an ext4 rootfs, and
booted a microVM with its own Linux kernel. The command ran inside the guest.
The exit code and output came back to your terminal. Because this was a
one-shot run, microagent removed the temporary workspace afterwards.

## Related

- **Run an actual agent inside a microVM** - [run your first agent](/getting-started/cli/first-agent/).
- **Keep a workspace around between runs** - [persistent workspaces](/guides/persistent-workspaces/) covers `create`, `start`, `halt`, `connect`, `delete`.
- **Already fluent in Docker?** [Coming from Docker](/getting-started/coming-from-docker/) maps the commands you know.
