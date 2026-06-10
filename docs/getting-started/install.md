---
title: Install
description: Install microagent via Homebrew or build from source.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-10_

## Homebrew

```bash
brew install geoffbelknap/tap/microagent
```

This installs the current stable `microagent` CLI on Linux and macOS. It also installs
`microagent-supervisor` as a host-specific symlink. On Linux, that symlink
targets the Firecracker supervisor; on macOS, it targets the Apple
Virtualization.framework supervisor. Go programs can import the same packages
that back the CLI; start with the [library overview](/library/) if you are
embedding microagent rather than using it from a shell.

Only stable releases ship to Homebrew. Release candidates are validated with
local builds (see "From source" below) and the tag-gated live CI suites, not
a published formula.

## From source

You need Go 1.26 or later. On macOS you also need a Swift toolchain to build
the supervisor.

```bash
git clone https://github.com/geoffbelknap/microagent.git
cd microagent
scripts/dev/build-local.sh
.build/dev/microagent version
.build/dev/microagent doctor
```

`build-local.sh` writes a self-contained development build under `.build/dev/`.
The CLI reports a development version based on the current release line, such
as `0.1.46-8780315` or `0.1.46-8780315-dirty`, so it is obvious you are not
running the latest stable Homebrew build. The script derives the `0.1.46`
prefix from the latest stable tag, ignoring release-candidate and other
prerelease tags, then adds the short SHA. It also places the host supervisor
and Linux guest-init companion next to the CLI so the resolver can find them.

If you build by hand instead, set the CLI version explicitly:

```bash
go build -ldflags "-X main.version=dev-local" ./cmd/microagent
go build ./cmd/microagent-firecracker-supervisor  # Linux
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

- **Try it from the CLI** - [run your first microVM](/getting-started/cli/first-microvm/), then [run your first agent](/getting-started/cli/first-agent/), then [named workspaces](/getting-started/cli/named-workspaces/) for stop/resume.
- **Embed it from Go** - start with the [library overview](/library/), then [run microagent from a Go program](/getting-started/library/first-program/).
