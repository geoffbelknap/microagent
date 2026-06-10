---
title: microagent version
description: Print the microagent version.
---

<!-- docs-last-updated -->
_Last updated: 2026-06-10_

```text
microagent version
microagent --version
microagent -v
```

Prints the build version of `microagent`. Stable Homebrew builds report the
released version. Local builds made with `scripts/dev/build-local.sh` report a
development version in the form `<latest-stable>-<git-sha>`, with `-dirty`
appended when the worktree had uncommitted or untracked changes at build time.
The stable prefix comes from the latest stable version tag; release-candidate
and other prerelease tags are ignored. Source builds made without version
linker flags still report `dev`.

## Example

```bash
$ microagent version
microagent 0.1.46-8780315
```

## Related

- [CLI reference](/cli/)
