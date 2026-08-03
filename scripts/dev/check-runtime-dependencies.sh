#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# Resolved from the repository root at runtime.
# shellcheck disable=SC1091
source "$ROOT/scripts/dev/firecracker-release.env"

case "$PINNED_FIRECRACKER_VERSION" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "invalid PINNED_FIRECRACKER_VERSION: $PINNED_FIRECRACKER_VERSION" >&2; exit 1 ;;
esac

for digest in "$PINNED_FIRECRACKER_SHA256_X86_64" "$PINNED_FIRECRACKER_SHA256_AARCH64"; do
  case "$digest" in
    *[!0-9a-f]*|'') echo "invalid Firecracker SHA-256: $digest" >&2; exit 1 ;;
  esac
  [ "${#digest}" -eq 64 ] || {
    echo "invalid Firecracker SHA-256 length: $digest" >&2
    exit 1
  }
done

if [ "${1:-}" != "--latest" ]; then
  echo "runtime dependency pins are valid"
  exit 0
fi

headers=(-H "Accept: application/vnd.github+json")
if [ -n "${GITHUB_TOKEN:-}" ]; then
  headers+=(-H "Authorization: Bearer $GITHUB_TOKEN")
fi
latest="$(curl -fsSL "${headers[@]}" \
  https://api.github.com/repos/firecracker-microvm/firecracker/releases/latest |
  python3 -c 'import json, sys; print(json.load(sys.stdin)["tag_name"])')"

if [ "$PINNED_FIRECRACKER_VERSION" != "$latest" ]; then
  echo "Firecracker pin is stale: pinned=$PINNED_FIRECRACKER_VERSION latest=$latest" >&2
  exit 1
fi
echo "Firecracker pin is current: $PINNED_FIRECRACKER_VERSION"
