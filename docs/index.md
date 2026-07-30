---
title: microagent
description: Boot Linux microVMs from OCI images, from the CLI or from Go.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-30_

microagent boots Linux microVMs from the OCI images you already use. Use
the CLI or Go API to run commands inside them, move files in and out, and
manage their lifecycle. microagent handles the kernel, disk, and network
plumbing.

Linux and macOS are the supported host targets. On a new machine, start with
[`microagent doctor`](/cli/doctor/) so the install can tell you exactly what
the host is missing before you boot a workspace.

## Choose your path

New here? The [glossary](/concepts/glossary/) defines workspace, rootfs,
egress, and the rest of the vocabulary. Deciding whether microagent is the
right tool? [Choosing microagent](/getting-started/choosing-microagent/)
compares it with containers, raw Firecracker, Mac VM managers, and
hosted sandboxes.

- **Try the CLI:** [Quickstart](/getting-started/quickstart/) boots a microVM
  and runs a command inside it. If you already think in Docker commands,
  [coming from Docker](/getting-started/coming-from-docker/) maps the common
  verbs.
- **Embed from Go:** Start with the [library overview](/library/), then
  [run microagent from a Go program](/getting-started/library/first-program/).
- **Connect an agent or MCP client:** Launch the stdio endpoint with
  [`microagent serve mcp`](/cli/serve/).
- **Have a quick question:** the [FAQ](/getting-started/faq/) answers the
  ones newcomers ask most; [limitations](/concepts/limitations/) covers what
  microagent deliberately doesn't do.

## Sections

- [Getting started](/getting-started/quickstart/): install, quickstart, and the first-agent walkthrough.
- [FAQ](/getting-started/faq/): short answers to common questions, with links to the full story.
- [Guides](/guides/): step-by-step walkthroughs, from one-shot runs to services and snapshots.
- [CLI reference](/cli/): every subcommand.
- [Library](/library/): Go package overview, reference, and CLI-to-library mapping.
- [Backends & platform support](/concepts/backends/): host requirements per backend, network modes, storage, and state.
- [Limitations](/concepts/limitations/): the deliberate refusals and where to go instead.
- [Security](/security/): trust boundary and reporting.
- [Troubleshooting](/troubleshooting/): common failure modes, indexed by symptom.
