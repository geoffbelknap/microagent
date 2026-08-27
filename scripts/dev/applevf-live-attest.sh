#!/usr/bin/env bash
#
# Run the targeted Apple VF live suite on this Apple-silicon host and record
# the result as an `applevf-live` commit status on the exact commit tested.
#
# GitHub's hosted macOS runners cannot boot Apple VF microVMs, so the mac live
# lane runs manually. This script is the release evidence for that run: the
# tag-triggered Apple VF live workflow requires a successful `applevf-live`
# status on the tagged commit and fails the release check without one.
#
# Usage:
#   scripts/dev/applevf-live-attest.sh [--dry-run]
#
# --dry-run runs the suite but prints the status call instead of posting it.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# The minimum attested set: the same targeted list the Apple VF live workflow
# runs. Broader release validation is described in CONTRIBUTING.md.
SCENARIOS=(mcp-lifecycle networking-deep public-surface applevf-direct-console applevf-snapshot)

dry_run=0
case "${1:-}" in
  "") ;;
  --dry-run) dry_run=1 ;;
  *)
    echo "usage: $0 [--dry-run]" >&2
    exit 2
    ;;
esac

command -v gh >/dev/null 2>&1 || {
  echo "FAIL: gh (GitHub CLI) is required to record the attestation" >&2
  exit 2
}

# Attest exactly what was tested: a dirty tree would attest a commit that is
# not the code that ran, and an unpushed commit cannot carry a status.
if [ -n "$(git -C "$ROOT" status --porcelain)" ]; then
  echo "FAIL: working tree is not clean; commit or stash before attesting" >&2
  exit 1
fi
sha="$(git -C "$ROOT" rev-parse HEAD)"
repo="$(cd "$ROOT" && gh repo view --json nameWithOwner --jq .nameWithOwner)"
if ! gh api "repos/$repo/commits/$sha" --jq .sha >/dev/null 2>&1; then
  echo "FAIL: commit $sha is not on $repo; push it before attesting" >&2
  exit 1
fi

MICROAGENT_E2E_BACKEND=apple-vf \
  "$ROOT/scripts/dev/microagent-e2e.sh" --require-vm "${SCENARIOS[@]}"

description="targeted Apple VF live suite passed ($(date -u +%Y-%m-%dT%H:%MZ))"
if [ "$dry_run" = "1" ]; then
  printf 'dry run: would record status on %s@%s:\n' "$repo" "$sha"
  printf '  gh api repos/%s/statuses/%s -f state=success -f context=applevf-live -f description=%q\n' \
    "$repo" "$sha" "$description"
  exit 0
fi

gh api "repos/$repo/statuses/$sha" \
  -f state=success \
  -f context=applevf-live \
  -f description="$description" >/dev/null
printf 'recorded applevf-live attestation on %s@%s\n' "$repo" "$sha"
