#!/usr/bin/env bash
# Preflight for building microagent from source. Checks that the build tools
# exist before any build step runs. When brew is available and the shell is
# interactive, offers to install what's missing; otherwise reports the tool
# and an install command instead of a shell error halfway through a build.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

go_hint() {
  if command -v brew >/dev/null 2>&1; then
    printf 'brew install go, or https://go.dev/dl/'
  else
    printf 'https://go.dev/dl/'
  fi
}

# offer asks a yes/no question on the terminal. Declines automatically when
# there is no terminal to ask (CI, piped input) or no brew to act with.
offer() {
  local prompt="$1" reply
  [ -t 0 ] || return 1
  command -v brew >/dev/null 2>&1 || return 1
  printf '%s [Y/n] ' "$prompt" >&2
  IFS= read -r reply || return 1
  case "$reply" in
    "" | y | Y | yes | YES) return 0 ;;
    *) return 1 ;;
  esac
}

brew_install_go() {
  if brew list --formula go >/dev/null 2>&1; then
    brew upgrade go
  else
    brew install go
  fi
  hash -r
}

if ! command -v go >/dev/null 2>&1; then
  if offer "microagent needs Go to build. Install it with brew now?"; then
    brew_install_go
  fi
fi
if ! command -v go >/dev/null 2>&1; then
  echo "microagent needs Go to build, and go is not on PATH." >&2
  echo "  install: $(go_hint)" >&2
  exit 2
fi

go_min="$(awk '/^go /{print $2; exit}' "$ROOT/go.mod")"
go_current() {
  local v
  v="$(go env GOVERSION 2>/dev/null || true)"
  printf '%s' "${v#go}"
}
go_old() {
  [ -n "$go_min" ] && [ -n "$1" ] && ! printf '%s\n%s\n' "$go_min" "$1" | sort -V -C
}

go_have="$(go_current)"
if go_old "$go_have"; then
  if offer "microagent needs Go $go_min or newer; $(command -v go) is go $go_have. Update it with brew now?"; then
    brew_install_go
    go_have="$(go_current)"
  fi
fi
if go_old "$go_have"; then
  echo "microagent needs Go $go_min or newer; $(command -v go) is go $go_have." >&2
  echo "  update: $(go_hint)" >&2
  exit 2
fi

# git is only used to stamp the build's version; a build from a source
# archive (no git, or no .git directory) still works and reports 0.0.0+local.
if ! command -v git >/dev/null 2>&1; then
  echo "warning: git is not on PATH; the build will report version 0.0.0+local" >&2
fi
