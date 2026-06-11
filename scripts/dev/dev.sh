#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLI="$ROOT/.build/dev/microagent"
DEV_DIR="$ROOT/.build/dev"
LIBEXEC_DIR="$ROOT/.build/libexec"

relpath() {
  case "$1" in
    "$ROOT")
      printf '%s\n' .
      ;;
    "$ROOT"/*)
      printf '%s\n' "${1#"$ROOT"/}"
      ;;
    *)
      printf '%s\n' "$1"
      ;;
  esac
}

print_build_summary() {
  echo "Dev build:"
  echo "  CLI: $(relpath "$CLI")"
  if [ -f "$DEV_DIR/microagent-firecracker-supervisor" ]; then
    echo "  VMM supervisor: $(relpath "$DEV_DIR/microagent-firecracker-supervisor")"
    if [ -e "$LIBEXEC_DIR/firecracker" ]; then
      echo "  Host VMM: $(relpath "$LIBEXEC_DIR/firecracker")"
    else
      echo "  Host VMM: missing"
    fi
  elif [ -f "$DEV_DIR/microagent-applevf-supervisor" ]; then
    echo "  VMM supervisor: $(relpath "$DEV_DIR/microagent-applevf-supervisor")"
  fi
}

print_doctor_summary() {
  local json="$1"
  python3 - "$json" <<'PY'
import json
import sys

path = sys.argv[1]
try:
    with open(path) as fh:
        data = json.load(fh)
except Exception as exc:
    print(f"Host check failed: could not parse doctor output: {exc}")
    raise SystemExit(0)

if data.get("ok"):
    print("Host check: ready")
    raise SystemExit(0)

error = data.get("error") or "host is not ready"
print("Host check failed:")
print(f"  {error}")

PY
}

run_doctor() {
  set +e
  "$CLI" --json doctor >"$doctor_json" 2>"$doctor_err"
  doctor_status=$?
  set -e
}

bootstrap_host() {
  local make_cmd
  local -a install_args

  make_cmd="${MAKE:-make}"
  install_args=(--no-print-directory install QUIET=1 CHECK=0)
  if [ -x "$LIBEXEC_DIR/firecracker" ]; then
    install_args+=("FIRECRACKER=$LIBEXEC_DIR/firecracker")
  fi

  (cd "$ROOT" && "$make_cmd" "${install_args[@]}")
}

doctor_json="$(mktemp "${TMPDIR:-/tmp}/microagent-dev-doctor.XXXXXX.json")"
doctor_err="$(mktemp "${TMPDIR:-/tmp}/microagent-dev-doctor.XXXXXX.err")"
# shellcheck disable=SC2317
cleanup() {
  rm -f "$doctor_json" "$doctor_err"
}
trap cleanup EXIT

"$ROOT/scripts/dev/build-local.sh" --quiet
print_build_summary

run_doctor

if [ "$doctor_status" -eq 0 ]; then
  print_doctor_summary "$doctor_json"
  exit 0
fi

echo
print_doctor_summary "$doctor_json" >&2

if [ "${MICROAGENT_DEV_BOOTSTRAP:-prompt}" = "never" ]; then
  echo "Run 'make install' to bootstrap this host, then retry 'make dev'." >&2
  exit "$doctor_status"
fi

if [ ! -r /dev/tty ] || [ ! -w /dev/tty ] || [ "${CI:-}" = "true" ]; then
  echo "Run 'make install' to bootstrap this host, then retry 'make dev'." >&2
  exit "$doctor_status"
fi

printf 'Run "make install" now to bootstrap missing host dependencies? [y/N] ' >/dev/tty
IFS= read -r answer </dev/tty || answer=""
case "$answer" in
  y|Y|yes|YES|Yes)
    bootstrap_host
    "$ROOT/scripts/dev/build-local.sh" --quiet
    print_build_summary
    run_doctor
    print_doctor_summary "$doctor_json"
    exit "$doctor_status"
    ;;
  *)
    echo "Skipped host bootstrap. Run 'make install' when ready." >&2
    exit "$doctor_status"
    ;;
esac
