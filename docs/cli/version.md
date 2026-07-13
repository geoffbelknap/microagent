---
title: microagent version
description: Print the microagent version.
---

<!-- docs-last-updated -->
_Last updated: 2026-07-13_

```text
microagent version
microagent --version
microagent -v
```

`version` prints the build version of `microagent`. Stable Homebrew builds
report the released version. Checkout-local builds report how far past the
release they are, in the form `<latest-stable>+<commits-since>-g<git-sha>`,
plus the source commit's date - so you can tell an old build from a current
one without decoding the sha. `-dirty` is appended when the worktree had
uncommitted or untracked changes at build time. A checkout exactly on a
release tag reports just the release version. Source builds made without
version linker flags report `dev`.

## Examples

```bash
$ microagent version
microagent 0.8.6+15-g9c7ad3d (commit 2026-07-12)
```

## Flags

`version` takes no flags of its own.

See [global flags](/cli/#global-flags) for `--json`/`--text`/`--output`/`--mode`.

## Exit status

`version` exits `0`. In AX mode a failure is written as a structured error
envelope.

## Related

- [CLI reference](/cli/) - every command and the global flags
- [`doctor`](/cli/doctor/) - check the install the version belongs to
