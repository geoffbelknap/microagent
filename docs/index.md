---
title: microagent
description: Boot Linux microVMs from OCI images, from the CLI or from Go.
---

<!-- docs-last-updated -->
_Last updated: 2026-09-02_

microagent boots Linux microVMs from the OCI images you already use. Use
the CLI or Go API to run commands inside them, move files in and out, and
manage their lifecycle. microagent handles the kernel, disk, and network
plumbing.

Linux and macOS are the supported host targets. On a new machine, start with
[`microagent doctor`](cli/doctor.md) so the install can tell you exactly what
the host is missing before you boot a workspace.

## Choose your path

New here? The [glossary](concepts/glossary.md) defines workspace, rootfs,
egress, and the rest of the vocabulary. Deciding whether microagent is the
right tool? [Choosing microagent](getting-started/choosing-microagent.md)
compares it with containers, raw Firecracker, Mac VM managers, and
hosted sandboxes.

- **Try the CLI:** [Quickstart](getting-started/quickstart.md) boots a microVM
  and runs a command inside it. If you already think in Docker commands,
  [coming from Docker](getting-started/coming-from-docker.md) maps the common
  verbs.
- **Embed from Go:** Start with the [library overview](library/index.md), then
  [run microagent from a Go program](getting-started/library/first-program.md).
- **Connect an agent or MCP client:** Launch the stdio endpoint with
  [`microagent serve mcp`](cli/serve.md).
- **Have a quick question:** the [FAQ](getting-started/faq.md) answers the
  ones newcomers ask most; [limitations](concepts/limitations.md) covers what
  microagent deliberately doesn't do.

## Sections

- [Getting started](getting-started/quickstart.md): install, quickstart, and the first-agent walkthrough.
- [FAQ](getting-started/faq.md): short answers to common questions, with links to the full story.
- [Guides](guides/index.md): step-by-step walkthroughs, from one-shot runs to services and snapshots.
- [CLI reference](cli/index.md): every subcommand.
- [Library](library/index.md): Go package overview, reference, and CLI-to-library mapping.
- [Backends & platform support](concepts/backends.md): host requirements per backend, network modes, storage, and state.
- [Limitations](concepts/limitations.md): the deliberate refusals and where to go instead.
- [Security](security.md): what microagent enforces at the VM boundary, control by control, and how to report.
- [Troubleshooting](troubleshooting.md): common failure modes, indexed by symptom.
