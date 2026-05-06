---
title: Release process
description: Tagging microagent-kit and publishing through the Homebrew tap.
---

`microagent-kit` releases are source releases consumed by
[`geoffbelknap/homebrew-tap`](https://github.com/geoffbelknap/homebrew-tap).
The tap formula builds the CLI, guest init binary, and backend supervisor from
the tagged source revision, then Homebrew builds bottles from the formula PR.

This repository does not currently publish platform binary assets for each
`microagent-kit` tag. The Homebrew tap is the distribution path operators use.

## Version Flow

1. Choose the next tag, for example `v0.1.32`.
2. Run the repository release checks:

   ```bash
   scripts/dev/release-check.sh
   ```

3. Run host-specific live checks on a machine with the right backend support:

   ```bash
   scripts/dev/release-check.sh --live
   ```

4. Tag the validated commit:

   ```bash
   git tag v0.1.32
   git push origin v0.1.32
   ```

5. Update `geoffbelknap/homebrew-tap`:

   - edit `microagent-kit.rb`
   - set `tag:` to the new `microagent-kit` tag
   - set `revision:` to the tagged commit SHA
   - keep kernel and Firecracker resources pinned by checksum

6. Open a PR in `geoffbelknap/homebrew-tap`.
7. Let `brew test-bot` build and upload bottle artifacts.
8. When the tap PR is ready, apply the `pr-pull` label so the tap workflow runs
   `brew pr-pull` and writes bottle checksums back to `main`.
9. Verify installation from the tap:

   ```bash
   brew update
   brew reinstall geoffbelknap/tap/microagent-kit
   microagent version
   microagent doctor
   ```

## Required Repository Checks

`scripts/dev/release-check.sh` runs checks that do not require live
virtualization:

- `go test ./...`
- `go vet ./...`
- `go test -race ./...`
- `make smoke-contract`
- internal Markdown link and CLI docs checks when the helpers are present
- shell syntax checks for scripts
- `shellcheck` when installed
- `govulncheck` when the Go tool can download it

The `--live` mode runs:

- `make smoke`
- `make smoke-rootfs`

`make smoke` selects the host backend: Firecracker on Linux and Apple VF on
macOS.

## Tap Formula Expectations

The Homebrew formula should:

- build `microagent` with `-X main.version=#{version}`
- build Linux `microagent-guestinit` for the host CPU family
- build and ad-hoc sign `microagent-applevf-supervisor` on macOS
- vendor Firecracker on Linux
- install default kernels from pinned URLs and SHA-256 checksums
- include formula tests for `microagent version`, rootfs validation errors,
  kernel help, guest init presence, and backend binaries

## Release Notes

Keep release notes short and concrete. Record operator validation
details under [`docs/releases/`](../releases/) when a release proves a new
backend, kernel, or lifecycle capability.
