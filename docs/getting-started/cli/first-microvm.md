---
title: Run your first microVM
description: Boot a Linux microVM, run a command, tear it down.
---

The fastest way to see microagent-kit work: boot a Linux microVM from an OCI
image, run one command inside it, and tear it down.

## Before you start

1. [Install microagent-kit](/getting-started/install/).
2. Run `microagent doctor` — it confirms the host has the right backend
   (Firecracker on Linux, Apple Virtualization.framework on macOS) and reports
   whether the default kernel is in place.

## Boot, run, tear down

```bash
microagent run \
  --image docker.io/library/ubuntu:24.04 \
  --exec "uname -a"
```

`--image` is the OCI image microagent converts into the rootfs. `--exec` is
the command to run inside the booted VM. The output looks something like:

```text
Linux microagent 6.1.0 #1 SMP ... x86_64 GNU/Linux
```

The first run also downloads the default kernel for the host backend; later
runs reuse it.

## What's next

- **Keep a workspace around between runs** — see [named workspaces](/getting-started/cli/named-workspaces/) for `create`, `start`, `halt`, `connect`, `delete`.
- **Run an actual agent inside the microVM** — see [run your first agent](/getting-started/cli/first-agent/).
- **Drive microagent from Go instead of the CLI** — see the [library quickstart](/getting-started/library/first-program/).
