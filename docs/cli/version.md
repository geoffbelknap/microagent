---
title: microagent version
description: Print the microagent version.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-23_

```text
microagent version
microagent --version
microagent -v
```

`version` prints the build version of `microagent`. Stable Homebrew builds
report the released version. Checkout-local builds append one build-metadata
block of dot-separated fields:

```text
<release>+<commits-since-release>.<git-sha>.<commit-date>[.dirty]
```

`0.8.6+15.9c7ad3d.20260712` is 15 commits past the v0.8.6 release, built from
commit `9c7ad3d`, committed 2026-07-12 - old builds are tellable from current
ones without decoding a sha. `.dirty` marks uncommitted or untracked changes
at build time. A clean checkout exactly on a release tag reports just the
release version. A build from a source archive rather than a git clone (a
release tarball or ZIP has no git metadata) reports `0.0.0+local`. Source
builds made without version linker flags report `dev`.

## Examples

```bash
$ microagent version
microagent 0.8.6+15.9c7ad3d.20260712
```

## Flags

`version` takes no flags of its own.

See [global flags](/cli/#global-flags) for `--output`/`--json`/`--mode`.

## Exit status

`version` exits `0`. In AX mode a failure is written as a structured error
envelope.

## Related

- [CLI reference](/cli/) - every command and the global flags
- [`doctor`](/cli/doctor/) - check the install the version belongs to
