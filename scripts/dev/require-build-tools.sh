#!/usr/bin/env bash
# Preflight for building microagent from source. Checks that the build tools
# exist before any build step runs, so a missing tool is reported as "install
# X" instead of a shell error halfway through a build script.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

install_hint() {
  local tool="$1"
  local hints=()
  if command -v brew >/dev/null 2>&1; then
    hints+=("brew install $tool")
  fi
  case "$tool" in
    go)
      # No apt hint on purpose: distro Go packages usually trail the version
      # go.mod requires.
      hints+=("https://go.dev/dl/")
      ;;
    git)
      if command -v apt-get >/dev/null 2>&1; then
        hints+=("sudo apt-get install git")
      fi
      ;;
  esac
  local joined=""
  local hint
  for hint in "${hints[@]}"; do
    if [ -n "$joined" ]; then
      joined="$joined, or $hint"
    else
      joined="$hint"
    fi
  done
  printf '%s' "$joined"
}

missing=()
command -v git >/dev/null 2>&1 || missing+=("git")
command -v go >/dev/null 2>&1 || missing+=("go")

if [ "${#missing[@]}" -gt 0 ]; then
  echo "microagent needs these build tools, which are not on PATH:" >&2
  for tool in "${missing[@]}"; do
    echo "  $tool  (install: $(install_hint "$tool"))" >&2
  done
  exit 2
fi

go_min="$(awk '/^go /{print $2; exit}' "$ROOT/go.mod")"
go_have="$(go env GOVERSION 2>/dev/null || true)"
go_have="${go_have#go}"
if [ -n "$go_min" ] && [ -n "$go_have" ]; then
  if ! printf '%s\n%s\n' "$go_min" "$go_have" | sort -V -C; then
    echo "microagent needs Go $go_min or newer; $(command -v go) is go $go_have." >&2
    echo "Update Go: $(install_hint go)" >&2
    exit 2
  fi
fi
