#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

usage() {
  cat >&2 <<'USAGE'
usage: scripts/dev/release-check.sh [--live]

Runs pre-tag release checks. By default this avoids live VM boot requirements.
Use --live on a host with the right virtualization support.
USAGE
}

live=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --live)
      live=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage
      exit 2
      ;;
  esac
  shift
done

cd "$ROOT"

if [ "$live" -eq 1 ]; then
  make smoke
  make smoke-rootfs
  exit 0
fi

go test ./...
go vet ./...
go test -race ./...
make smoke-contract

if [ -x scripts/dev/markdown-link-check.py ]; then
  python3 scripts/dev/markdown-link-check.py
fi
if [ -x scripts/dev/cli-docs-check.py ]; then
  python3 scripts/dev/cli-docs-check.py
fi

find scripts -name '*.sh' -print0 | xargs -0 -n1 bash -n

if command -v shellcheck >/dev/null 2>&1; then
  find scripts -name '*.sh' -print0 | xargs -0 shellcheck
else
  echo "shellcheck not found; skipping shell lint" >&2
fi

go run golang.org/x/vuln/cmd/govulncheck@latest ./...
