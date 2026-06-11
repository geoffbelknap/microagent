---
title: Install
description: Install microagent with Homebrew or build it from source.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

Install the `microagent` CLI, then verify the host can boot microVMs with
`microagent doctor`. Homebrew is the fast path on Linux and macOS; build from
source when you need a specific checkout.

## Homebrew

```bash
brew install geoffbelknap/tap/microagent
```

This installs the current stable `microagent` CLI plus `microagent-supervisor`
as a host-specific symlink: Firecracker supervisor on Linux, Apple
Virtualization.framework supervisor on macOS. Go programs can import the same
packages that back the CLI; start with the [library overview](/library/) if
you are embedding microagent rather than using it from a shell.

Only stable releases ship to Homebrew. Release candidates are validated with
local builds (see "From source" below) and the tag-gated live CI suites, not a
published formula.

## From source

You need Go 1.26 or later. On macOS you also need a Swift toolchain to build
the supervisor.

To install from a checkout into a normal Unix prefix:

```bash
git clone https://github.com/geoffbelknap/microagent.git
cd microagent
make install
export PATH="$HOME/.local/bin:$PATH"
microagent doctor
```

By default, `make install` installs under `$HOME/.local`. Use
`PREFIX=/usr/local` for a system-style install; that may require running the
script with privileges. The installer writes the packaged layout that
microagent's resolvers expect: `bin/microagent`, supervisor symlinks in
`bin/`, and companion artifacts under `libexec/`.

On Linux, `make install` also downloads the pinned upstream Firecracker VMM
release, verifies the archive SHA-256, and installs it as
`PREFIX/libexec/firecracker`. This mirrors the Homebrew formula: the binary is
a packaged external resource, not a blob committed to the microagent source
repository.

The installer also tries to install required host packages through the system
package manager:

- Linux: `passt` for `pasta`, plus `e2fsprogs` for `mke2fs`.
- macOS: `e2fsprogs` through Homebrew when `mke2fs` is missing.

After installing packages, the source installer validates that `pasta` and
`mke2fs` are executable on Linux. If `passt` installs successfully but does
not put `pasta` on `PATH`, the install fails at that step instead of leaving
the problem for a later workspace start.

Package installation may prompt for `sudo`. To skip package-manager changes:

```bash
make install INSTALL_HOST_PACKAGES=0
```

`make install` prints a compact install summary by default. Package-manager
and download details are written to a temporary log when quiet mode is
enabled; the installer prints that log path if a command fails. Set
`MICROAGENT_INSTALL_LOG=/path/to/install.log` to choose the log location, or
use `QUIET=0` to stream command output directly.

If you already have a Firecracker binary, install it into the same prefix:

```bash
make install FIRECRACKER=/path/to/firecracker
```

To skip the Firecracker download and rely on `PATH` or `MICROAGENT_FIRECRACKER`:

```bash
make install DOWNLOAD_FIRECRACKER=0
```

By default, `make install` installs microagent's default kernel into
`PREFIX/libexec/kernels/<backend>/<arch>/Image`, so the install behaves like a
packaged install. Use `INSTALL_KERNEL=0` to skip that step; the first
workspace create or run can still install the default writable kernel under
`~/.microagent/kernels/...`.

Some host capabilities cannot be installed by a source checkout. Linux still
needs `/dev/kvm` access, and some distros require enabling unprivileged user
namespaces for `pasta`. The final `microagent doctor` run reports those gaps
with concrete remediation. For packaging or staged installs, `CHECK=0` skips
that final check and `ARCH=<arch>` selects the guest architecture.

## Developer build

For a local build that stays inside the checkout:

```bash
git clone https://github.com/geoffbelknap/microagent.git
cd microagent
make dev
.build/dev/microagent version
```

`make dev` writes a self-contained development build under `.build/dev/`, then
runs `.build/dev/microagent doctor` so missing host prerequisites are visible.
If the host is not ready and the command is running in an interactive
terminal, it offers to run `make install` in quiet bootstrap mode, reusing the
dev-linked Host VMM when one is present. In CI or other non-interactive
shells, it prints that command and exits with the doctor failure.

The CLI reports a development version based on the current release line, such
as `0.1.46-8780315` or `0.1.46-8780315-dirty`, so it is obvious you are not
running the latest stable Homebrew build. The script derives the `0.1.46`
prefix from the latest stable tag, ignoring release-candidate and other
prerelease tags, then adds the short SHA. It also places the host supervisor
and Linux guest-init companion next to the CLI so the resolver can find them.
When an installed Firecracker VMM is available, the dev build links it under
`.build/libexec/firecracker` so the checkout-local CLI can resolve it the same
way as a packaged install.

The script prints the absolute CLI path and a shell `PATH` export you can use
for interactive development. MCP clients do not inherit changes made inside
the build script, so configure them either with that absolute CLI path or
restart the client from a shell where `.build/dev` is already on `PATH`.

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

Boot your first microVM: the [quickstart](/getting-started/quickstart/) takes
it from here.
