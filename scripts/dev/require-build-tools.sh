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

if ! command -v go >/dev/null 2>&1; then
  echo "microagent needs Go to build, and go is not on PATH." >&2
  echo "  install: $(install_hint go)" >&2
  exit 2
fi

# git is only used to stamp the build's version; a build from a source
# archive (no git, or no .git directory) still works and reports 0.0.0+local.
if ! command -v git >/dev/null 2>&1; then
  echo "warning: git is not on PATH; the build will report version 0.0.0+local (install: $(install_hint git))" >&2
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
