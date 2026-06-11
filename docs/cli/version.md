---
title: microagent version
description: Print the microagent version.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-11_

```text
microagent version
microagent --version
microagent -v
```

`version` prints the build version of `microagent`. Stable Homebrew builds
report the released version. Local builds made with
`scripts/dev/build-local.sh` report a development version in the form
`<latest-stable>-<git-sha>`, with `-dirty` appended when the worktree had
uncommitted or untracked changes at build time. The stable prefix comes from
the latest stable version tag; release-candidate and other prerelease tags are
ignored. Source builds made without version linker flags report `dev`.

## Examples

```bash
$ microagent version
microagent 0.1.46-8780315
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
