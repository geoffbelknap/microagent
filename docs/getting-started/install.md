---
title: Install
description: Install microagent via Homebrew or build from source.
---

<!-- docs-last-updated -->
_Last updated: 2026-05-17_

## Homebrew

```bash
brew install geoffbelknap/tap/microagent
```

This installs the `microagent` CLI on Linux and macOS. It also installs
`microagent-supervisor` as a host-specific symlink. On Linux, that symlink
targets the Firecracker supervisor; on macOS, it targets the Apple
Virtualization.framework supervisor.

## From source

You need Go 1.26 or later. On macOS you also need a Swift toolchain to build
the supervisor.

```bash
git clone https://github.com/geoffbelknap/microagent.git
cd microagent
go build ./cmd/microagent ./cmd/microagent-firecracker-supervisor  # Linux
go build ./cmd/microagent                                         # macOS
swift build --package-path supervisors/applevf --disable-sandbox  # macOS only
```

To produce an ad-hoc signed supervisor (macOS):

```bash
make signed-supervisor
```

## Verify the host

```bash
microagent doctor
```

`doctor` checks for the right backend on the current host: Firecracker plus
`/dev/kvm` on Linux, Apple Virtualization.framework on macOS. It also reports
default kernel status. Run it outside sandboxed environments on Linux so KVM
visibility is honest.

## Next

- **Try it from the CLI** — [run your first microVM](/getting-started/cli/first-microvm/), then [run your first agent](/getting-started/cli/first-agent/), then [named workspaces](/getting-started/cli/named-workspaces/) for stop/resume.
- **Build with the library** — [run microagent from a Go program](/getting-started/library/first-program/).
