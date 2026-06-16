---
title: microagent version
description: Print the microagent version.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-16_

```text
microagent version
microagent --version
microagent -v
```

`version` prints the build version of `microagent`. Stable Homebrew builds
report the released version. Checkout-local builds report a source version in
the form `<latest-stable>-<git-sha>`, with `-dirty` appended when the worktree
had uncommitted or untracked changes at build time. Source builds made without
version linker flags report `dev`.

## Examples

```bash
$ microagent version
microagent 0.8.0-8780315
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
