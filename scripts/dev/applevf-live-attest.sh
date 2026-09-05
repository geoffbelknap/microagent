#!/usr/bin/env bash
# Compatibility entry point: qualify this revision in an isolated checkout.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
record=(--record)
case "${1:-}" in
  "") ;;
  --dry-run) record=() ;;
  -h|--help)
    echo "usage: scripts/dev/applevf-live-attest.sh [--dry-run]"
    echo "Validates HEAD in an isolated checkout; --dry-run withholds GitHub status."
    exit 0 ;;
  *) echo "usage: scripts/dev/applevf-live-attest.sh [--dry-run]" >&2; exit 2 ;;
esac
if [ -n "$(git -C "$ROOT" status --porcelain)" ]; then
  echo "FAIL: working tree is not clean; commit changes before qualifying HEAD" >&2
  exit 1
fi
sha="$(git -C "$ROOT" rev-parse HEAD)"
exec python3 "$ROOT/scripts/dev/qualify-applevf.py" --ref "$sha" "${record[@]}"
