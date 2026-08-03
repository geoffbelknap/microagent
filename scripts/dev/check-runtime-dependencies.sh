#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# Resolved from the repository root at runtime.
# shellcheck disable=SC1091
source "$ROOT/scripts/dev/firecracker-release.env"

go_version="$(tr -d '[:space:]' <"$ROOT/.go-version")"
case "$go_version" in
  [0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "invalid .go-version: $go_version" >&2; exit 1 ;;
esac

golangci_version="$(sed -n '/golangci\/golangci-lint-action/,/version:/ {
  s/^[[:space:]]*version:[[:space:]]*//p
}' "$ROOT/.github/workflows/ci.yaml" | head -n 1)"
case "$golangci_version" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "invalid golangci-lint version: $golangci_version" >&2; exit 1 ;;
esac

python3 - "$ROOT/pkg/kernel/trusted_root.json" <<'PY'
import datetime
import json
import pathlib
import sys

root = json.loads(pathlib.Path(sys.argv[1]).read_text())
expires = datetime.datetime.fromisoformat(root["signed"]["expires"].replace("Z", "+00:00"))
remaining = expires - datetime.datetime.now(datetime.timezone.utc)
if remaining < datetime.timedelta(days=120):
    raise SystemExit(f"embedded kernel TUF root expires too soon: {expires.isoformat()}")
print(f"kernel TUF root valid until {expires.date()}")
PY

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

latest_go="$(curl -fsSL https://go.dev/dl/?mode=json |
  python3 -c 'import json, sys; print(json.load(sys.stdin)[0]["version"].removeprefix("go"))')"
if [ "$go_version" != "$latest_go" ]; then
  echo "Go pin is stale: pinned=$go_version latest=$latest_go" >&2
  exit 1
fi
echo "Go pin is current: $go_version"

latest_golangci="$(curl -fsSL "${headers[@]}" \
  https://api.github.com/repos/golangci/golangci-lint/releases/latest |
  python3 -c 'import json, sys; print(json.load(sys.stdin)["tag_name"])')"
if [ "$golangci_version" != "$latest_golangci" ]; then
  echo "golangci-lint pin is stale: pinned=$golangci_version latest=$latest_golangci" >&2
  exit 1
fi
echo "golangci-lint pin is current: $golangci_version"
