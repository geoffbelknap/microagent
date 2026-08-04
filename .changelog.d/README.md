# Changelog fragments

A change adds release notes here instead of editing
[`CHANGELOG.md`](../CHANGELOG.md) directly. Two PRs that each edit
`CHANGELOG.md` conflict on that one file even when they share no code; a
fragment per change means they never touch the same file.

Add one file per change: `.changelog.d/<short-slug>.md`, named after the
change itself (not an issue or task number). Write it exactly as it would
read in `CHANGELOG.md`: one or more `###` sections, same prose, same
heading level.

At release time, `scripts/dev/changelog-assemble.py` concatenates every
fragment into `CHANGELOG.md` under the `Unreleased` heading, in filename
order, and deletes the fragment files. Outside that step, this directory
holds only the fragments for changes that have not been released yet - it
is normal for it to be empty.

See [`CONTRIBUTING.md`](../CONTRIBUTING.md) for the rest of the PR
conventions.
