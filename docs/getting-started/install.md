---
title: Install
description: Install microagent-kit via Homebrew or build from source.
---

## Homebrew

```bash
brew install geoffbelknap/tap/microagent-kit
```

This installs the `microagent` CLI on Linux and macOS. On Linux it also
installs `microagent-firecracker-supervisor`. On macOS it also installs
`microagent-applevf-supervisor`, the Swift JSON executable that owns Apple
Virtualization.framework lifecycle.

## From source

You need Go 1.26 or later. On macOS you also need a Swift toolchain to build
the supervisor.

```bash
git clone https://github.com/geoffbelknap/microagent-kit.git
cd microagent-kit
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

- [Run your first VM](first-vm.md)
- [Named workspaces](named-workspaces.md)
