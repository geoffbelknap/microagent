#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLI="$ROOT/.build/dev/microagent"
DEV_DIR="$ROOT/.build/dev"
LIBEXEC_DIR="$ROOT/.build/libexec"
# Where 'make install' puts the CLI (Makefile: PREFIX ?= $(HOME)/.local).
INSTALL_BIN="${PREFIX:-$HOME/.local}/bin"
INSTALL_TARGET="$INSTALL_BIN/microagent"

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

# After 'make install' writes $INSTALL_TARGET, would the shell pick it up?
# True when $INSTALL_BIN comes before any other microagent on PATH.
install_target_wins_path() {
  local dir
  local IFS=:
  for dir in $PATH; do
    [ -n "$dir" ] || dir=.
    if [ "$dir" = "$INSTALL_BIN" ] || [ "$dir" -ef "$INSTALL_BIN" ]; then
      return 0
    fi
    if [ -x "$dir/microagent" ]; then
      return 1
    fi
  done
  return 1
}

check_shell_command() {
  local resolved
  local cli_version
  local path_version
  local path_status

  cli_version="$("$CLI" -v)"
  if ! resolved="$(command -v microagent 2>/dev/null)"; then
    echo "Shell command: missing"
    echo "  Run 'make install' to install microagent on PATH, or use:"
    echo "  export PATH=\"$DEV_DIR:\$PATH\""
    return 1
  fi

  set +e
  path_version="$("$resolved" -v 2>/dev/null)"
  path_status=$?
  set -e
  if [ "$path_status" -ne 0 ]; then
    echo "Shell command: $resolved"
    echo "  Installed command exists but failed to report a version."
    return 1
  fi

  if [ "$path_version" != "$cli_version" ]; then
    echo "Shell command: $resolved ($path_version)"
    echo "  Dev build: $cli_version"
    if install_target_wins_path; then
      echo "  Run 'make install' to update the command on PATH, or use:"
      echo "  export PATH=\"$DEV_DIR:\$PATH\""
      return 1
    fi
    echo "  Your shell finds $resolved first, which is not the copy"
    echo "  'make install' manages ($INSTALL_TARGET), so 'make install' cannot fix this."
    case "$resolved" in
      */linuxbrew/*|/opt/homebrew/*|*/Homebrew/*|/usr/local/Cellar/*)
        echo "  Remove the Homebrew copy:"
        echo "    brew uninstall microagent"
        echo "  Or keep it and put the installed copy first in PATH:"
        ;;
      *)
        echo "  Remove that copy, or put the installed copy first in PATH:"
        ;;
    esac
    echo "    export PATH=\"$INSTALL_BIN:\$PATH\""
    return 2
  fi

  echo "Shell command: $resolved ($path_version)"
  return 0
}

print_shell_cache_hint() {
  echo "If this shell still tries an old microagent path, run: hash -r" >&2
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

doctor_json="$(mktemp "${TMPDIR:-/tmp}/microagent-dev-doctor-json.XXXXXX")"
doctor_err="$(mktemp "${TMPDIR:-/tmp}/microagent-dev-doctor-err.XXXXXX")"
# shellcheck disable=SC2317,SC2329
cleanup() {
  rm -f "$doctor_json" "$doctor_err"
}
trap cleanup EXIT

"$ROOT/scripts/dev/build-local.sh" --quiet
print_build_summary

run_doctor

if [ "$doctor_status" -eq 0 ]; then
  print_doctor_summary "$doctor_json"
  set +e
  check_shell_command
  shell_status=$?
  set -e
  if [ "$shell_status" -eq 1 ]; then
    if [ "${MICROAGENT_DEV_BOOTSTRAP:-prompt}" = "never" ]; then
      exit 0
    fi
    if [ -r /dev/tty ] && [ -w /dev/tty ] && [ "${CI:-}" != "true" ]; then
      printf 'Run "make install" now to update the microagent command on PATH? [Y/n] ' >/dev/tty
      IFS= read -r answer </dev/tty || answer=""
      case "$answer" in
        n|N|no|NO|No)
          echo "Skipped command install. Use '.build/dev/microagent' or update PATH when ready." >&2
          ;;
        *)
          bootstrap_host
          "$ROOT/scripts/dev/build-local.sh" --quiet
          print_build_summary
          run_doctor
          print_doctor_summary "$doctor_json"
          set +e
          check_shell_command
          shell_status=$?
          set -e
          if [ "$shell_status" -ne 2 ]; then
            print_shell_cache_hint
          fi
          exit "$doctor_status"
          ;;
      esac
    fi
  fi
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

printf 'Run "make install" now to bootstrap missing host dependencies? [Y/n] ' >/dev/tty
IFS= read -r answer </dev/tty || answer=""
case "$answer" in
  n|N|no|NO|No)
    echo "Skipped host bootstrap. Run 'make install' when ready." >&2
    exit "$doctor_status"
    ;;
  *)
    bootstrap_host
    "$ROOT/scripts/dev/build-local.sh" --quiet
    print_build_summary
    run_doctor
    print_doctor_summary "$doctor_json"
    set +e
    check_shell_command
    shell_status=$?
    set -e
    if [ "$shell_status" -ne 2 ]; then
      print_shell_cache_hint
    fi
    exit "$doctor_status"
    ;;
esac
