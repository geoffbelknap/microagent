# Homebrew packaging sources

<!-- docs-last-updated -->
_Last updated: 2026-07-28_

The tap formulas for microagent live in the public `homebrew-tap`
repository, but their caveat text is owned here: on every formula bump,
the release workflows rewrite the formula's `caveats` block from these
files, so install-time text changes land in the same pull request as the
behavior change that motivates them.

- `microagent.caveats` — stable formula. An empty file means the formula
  has no caveats block at all (a clean install needs nothing and says
  nothing).
- `microagent-latest.caveats` — latest-train formula.

The rewriting is done by `scripts/dev/update-tap-formula.py`, called from
`.github/workflows/latest.yaml` (every merge to main) and
`.github/workflows/update-homebrew-tap.yml` (stable releases).
