#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PREFIX="${MICROAGENT_FIRECRACKER_PERF_PREFIX:-${TMPDIR:-/tmp}/microagent-firecracker-perf}"
BIN_DIR="$PREFIX/bin"
LIBEXEC_DIR="$PREFIX/libexec"
KERNEL_DIR="$LIBEXEC_DIR/kernels/firecracker/amd64"

usage() {
  cat >&2 <<'EOF'
Usage: scripts/dev/firecracker-perf-helper.sh <microagent args...>

Builds the current checkout into a disposable packaged layout and runs the
resulting microagent binary with that layout first on PATH.

Examples:
  scripts/dev/firecracker-perf-helper.sh perf boot
  scripts/dev/firecracker-perf-helper.sh create perf-main --state-dir /tmp/microagent-perf-state
  scripts/dev/firecracker-perf-helper.sh start perf-main --state-dir /tmp/microagent-perf-state
  scripts/dev/firecracker-perf-helper.sh perf footprint perf-main --state-dir /tmp/microagent-perf-state
  scripts/dev/firecracker-perf-helper.sh perf steady perf-main --duration 5 --interval 1 --state-dir /tmp/microagent-perf-state
EOF
}

find_firecracker() {
  if [ -n "${MICROAGENT_FIRECRACKER:-}" ]; then
    printf '%s\n' "$MICROAGENT_FIRECRACKER"
    return
  fi
  if command -v firecracker >/dev/null 2>&1; then
    command -v firecracker
    return
  fi
  if command -v brew >/dev/null 2>&1; then
    formula_prefix="$(brew --prefix microagent-kit 2>/dev/null || true)"
    if [ -n "$formula_prefix" ] && [ -x "$formula_prefix/libexec/firecracker" ]; then
      printf '%s\n' "$formula_prefix/libexec/firecracker"
      return
    fi
  fi
  return 1
}

find_kernel() {
  if [ -n "${MICROAGENT_FIRECRACKER_KERNEL:-}" ]; then
    printf '%s\n' "$MICROAGENT_FIRECRACKER_KERNEL"
    return
  fi
  for candidate in \
    "$HOME/.microagent/kernels/firecracker/amd64/Image" \
    "$HOME/.microagent/kernels/firecracker/Image"; do
    if [ -f "$candidate" ]; then
      printf '%s\n' "$candidate"
      return
    fi
  done
  if command -v brew >/dev/null 2>&1; then
    formula_prefix="$(brew --prefix microagent-kit 2>/dev/null || true)"
    candidate="$formula_prefix/libexec/kernels/firecracker/amd64/Image"
    if [ -n "$formula_prefix" ] && [ -f "$candidate" ]; then
      printf '%s\n' "$candidate"
      return
    fi
  fi
  return 1
}

if [ "$#" -eq 0 ] || [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 2
fi

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    echo "firecracker perf helper requires Linux amd64" >&2
    exit 2
    ;;
esac

firecracker="$(find_firecracker || true)"
if [ ! -x "${firecracker:-}" ]; then
  echo "firecracker binary not found; install microagent-kit or set MICROAGENT_FIRECRACKER" >&2
  exit 2
fi

kernel="$(find_kernel || true)"
if [ ! -f "${kernel:-}" ]; then
  echo "Firecracker kernel not found; run microagent kernel install or set MICROAGENT_FIRECRACKER_KERNEL" >&2
  exit 2
fi

mkdir -p "$BIN_DIR" "$LIBEXEC_DIR" "$KERNEL_DIR"

export GOCACHE="${GOCACHE:-$PREFIX/gocache}"
export GOMODCACHE="${GOMODCACHE:-$PREFIX/gomodcache}"
export GOFLAGS="${GOFLAGS:-} -modcacherw"

(
  cd "$ROOT"
  go build -o "$BIN_DIR/microagent" ./cmd/microagent
  go build -o "$BIN_DIR/microagent-firecracker-supervisor" ./cmd/microagent-firecracker-supervisor
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$LIBEXEC_DIR/microagent-guestinit-amd64" ./cmd/microagent-guestinit
)

cp "$firecracker" "$LIBEXEC_DIR/firecracker"
cp "$kernel" "$KERNEL_DIR/Image"

export PATH="$BIN_DIR:$PATH"
exec "$BIN_DIR/microagent" "$@"
