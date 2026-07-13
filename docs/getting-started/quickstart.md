---
title: Quickstart
description: Boot a Linux microVM from an OCI image and run a command inside it.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-13_

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

While the image pulls and the microVM boots, progress is shown on stderr. Then
the command's output arrives exactly as if you had run it locally:

```text
Linux run-lively-heron-7q3f 6.1.155 #2 SMP PREEMPT_DYNAMIC Sat May  2 18:32:03 UTC 2026 x86_64 x86_64 x86_64 GNU/Linux
```

The guest command's exit code becomes `microagent`'s exit code, and stdout
carries only the command's output, so pipes and `$?` work the way you expect.
Add `--json` to get the full structured result (workspace name, resources,
kernel, exit code, captured output).

If you run an image without a command, microagent uses the image's
Entrypoint/Cmd.

## What just happened

microagent pulled the Ubuntu image, converted it into an ext4 rootfs, and
booted a microVM with its own Linux kernel. The command ran inside the guest.
The exit code and output came back to your terminal. Because this was a
one-shot run, microagent removed the temporary workspace afterwards.

## Related

- [Run your first agent](/getting-started/cli/first-agent/) — put an agent inside a microVM.
- [Persistent workspaces](/guides/persistent-workspaces/) — keep a workspace around between runs, with `create`, `start`, `halt`, `connect`, `delete`.
- [Coming from Docker](/getting-started/coming-from-docker/) — maps the commands you already know.
- [Glossary](/concepts/glossary/) — defines workspace, rootfs, egress, and the rest of the vocabulary.
