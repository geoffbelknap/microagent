---
title: microagent
description: Boot real Linux microVMs from OCI images, from the CLI or from Go.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-13_

microagent boots real Linux microVMs from the OCI images you already use. Use
the CLI or Go API to run commands inside them, move files in and out, and
manage their lifecycle. microagent handles the kernel, disk, and network
plumbing.

Linux and macOS are the supported host targets. On a new machine, start with
[`microagent doctor`](/cli/doctor/) so the install can tell you exactly what
the host is missing before you boot a workspace.

## Choose your path

New here? The [glossary](/concepts/glossary/) defines workspace, rootfs,
egress, and friends.

- **Try the CLI:** [Quickstart](/getting-started/quickstart/) boots a microVM
  and runs a command inside it. If you already think in Docker commands,
  [coming from Docker](/getting-started/coming-from-docker/) maps the common
  verbs.
- **Embed from Go:** Start with the [library overview](/library/), then
  [run microagent from a Go program](/getting-started/library/first-program/).
- **Connect an agent or MCP client:** Launch the stdio endpoint with
  [`microagent serve mcp`](/cli/serve/).

## Sections

- [Getting started](/getting-started/quickstart/): install, quickstart, and the first-agent walkthrough.
- [Guides](/guides/): step-by-step walkthroughs, from one-shot runs to services and snapshots.
- [CLI reference](/cli/): every subcommand.
- [Library](/library/): Go package overview, reference, and CLI-to-library mapping.
- [Backends & platform support](/concepts/backends/): host requirements per backend, network modes, storage, and state.
- [Security](/security/): trust boundary and reporting.
- [Troubleshooting](/troubleshooting/): common failure modes, indexed by symptom.
