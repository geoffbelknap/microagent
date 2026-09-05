# Homebrew packaging sources

<!-- docs-last-updated -->
_Last updated: 2026-09-05_

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

Each formula carries separate Linux and macOS source pins and caveats. Linux
stable advances on release publication; Linux latest advances on main merges.
Mac promotion is explicit and requires `applevf-qualified` on the selected
commit. See [Mac qualification](../../CONTRIBUTING.md#mac-qualification).

The updater preserves the other platform and all resource definitions. Its
first update splits the shared pin. The latest formula uses the stable Mac
source as its initial Mac pin. Promotions cannot move backward in source history.
