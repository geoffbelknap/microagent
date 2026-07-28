# shellcheck shell=bash
# Shared helpers for microagent end-to-end scenarios.
#
# Source this from a scenario script:  . "$ROOT/scripts/dev/e2e-lib.sh"
#
# The key contract is the SKIP convention: a scenario that cannot run because a
# host prerequisite is missing exits 77 (via e2e_skip). The runner
# (microagent-e2e.sh) treats exit 77 as SKIP, any other non-zero as FAIL, and 0
# as PASS, then prints a PASS/SKIP/FAIL summary. This lets the same suite run on
# a fresh Linux/WSL/macOS box and report what it validated vs. what it couldn't,
# instead of crashing on the first missing dependency.

E2E_SKIP_EXIT=77

e2e_step() { printf '\n--- %s ---\n' "$*"; }
e2e_log() { printf '%s\n' "$*"; }

# e2e_skip <reason>: report a prerequisite gap and exit with the SKIP code.
e2e_skip() {
  printf 'SKIP: %s\n' "$*" >&2
  exit "$E2E_SKIP_EXIT"
}

# e2e_fail <reason>: report an assertion failure and exit non-zero (FAIL).
e2e_fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

e2e_os() { uname -s; }
e2e_arch() { uname -m; }

# e2e_friendly_os: short OS name for preflight output.
e2e_friendly_os() {
  uname -s
}

# e2e_exe <path>: return the built executable path.
e2e_exe() {
  printf '%s\n' "$1"
}

# e2e_host_path <path>: print the host-native path.
e2e_host_path() {
  printf '%s\n' "$1"
}

# e2e_host_pid: print the host-native PID of this shell.
e2e_host_pid() {
  printf '%s\n' "$$"
}

# e2e_is_wsl: true on Windows Subsystem for Linux.
e2e_is_wsl() {
  case "$(uname -r 2>/dev/null)" in
    *microsoft*|*Microsoft*|*WSL*) return 0 ;;
  esac
  [ -n "${WSL_DISTRO_NAME:-}" ]
}

e2e_is_root() { [ "$(id -u)" -eq 0 ]; }

e2e_require_cmd() {
  command -v "$1" >/dev/null 2>&1 || e2e_skip "${2:-required command not found: $1}"
}

e2e_have_kvm() { [ -e /dev/kvm ] && [ -r /dev/kvm ]; }

# e2e_resolve_firecracker: print the firecracker binary path, or empty.
#
# microagent does NOT expect firecracker on $PATH — an install bundles it in
# libexec next to the microagent binary (its own resolution is env -> PATH ->
# <exe>/../libexec/firecracker). The e2e suite builds a dev CLI in a temp dir,
# so it can't use that relative libexec; instead it locates the bundled binary
# via an installed microagent and exports it as MICROAGENT_FIRECRACKER. Order:
#   1. explicit MICROAGENT_FIRECRACKER override
#   2. firecracker on PATH (uncommon, but honored)
#   3. ask an installed `microagent` where its bundled firecracker is (doctor)
#   4. derive the libexec sibling of an installed `microagent` binary
#   5. a Homebrew microagent formula's libexec
e2e_resolve_firecracker() {
  if [ -n "${MICROAGENT_FIRECRACKER:-}" ] && [ -x "${MICROAGENT_FIRECRACKER:-}" ]; then
    printf '%s\n' "$MICROAGENT_FIRECRACKER"
    return 0
  fi
  if command -v firecracker >/dev/null 2>&1; then
    command -v firecracker
    return 0
  fi
  if command -v microagent >/dev/null 2>&1; then
    # microagent doctor reports the firecracker path it resolved as host.binaryPath.
    fc="$(microagent --json doctor 2>/dev/null | sed -n 's/.*"binaryPath"[: ]*"\([^"]*\)".*/\1/p' | head -1)"
    if [ -n "$fc" ] && [ -x "$fc" ]; then
      printf '%s\n' "$fc"
      return 0
    fi
    mdir="$(dirname "$(readlink -f "$(command -v microagent)" 2>/dev/null || command -v microagent)")"
    if [ -n "$mdir" ] && [ -x "$mdir/../libexec/firecracker" ]; then
      printf '%s\n' "$mdir/../libexec/firecracker"
      return 0
    fi
  fi
  if command -v brew >/dev/null 2>&1; then
    prefix="$(brew --prefix microagent 2>/dev/null || true)"
    if [ -n "$prefix" ] && [ -x "$prefix/libexec/firecracker" ]; then
      printf '%s\n' "$prefix/libexec/firecracker"
      return 0
    fi
  fi
  return 1
}

# e2e_normalize_backend: canonicalize a backend lane value and reject one the
# harness does not know. The product's canonical names are "linux-kvm" and
# "apple-vf"; the legacy "applevf" spelling is accepted and normalized. An
# unknown lane FAILS instead of skipping: a skip exits 0 and reads as
# "suite OK", so a typo in MICROAGENT_E2E_BACKEND could silently pass a
# release validation that booted nothing.
e2e_normalize_backend() {
  case "$1" in
    linux-kvm | apple-vf) printf '%s\n' "$1" ;;
    applevf) printf '%s\n' apple-vf ;;
    *)
      echo "FAIL: unknown backend lane: $1 (known: linux-kvm, apple-vf)" >&2
      return 1
      ;;
  esac
}

e2e_have_applevf() {
  [ "$(uname -s)" = "Darwin" ] || return 1
  [ "$(uname -m)" = "arm64" ] || return 1

  supervisor="${MICROAGENT_APPLEVF_SUPERVISOR:-${ROOT:-$(pwd)}/supervisors/applevf/.build/release/microagent-applevf-supervisor}"
  [ -x "$supervisor" ] || return 1

  response="$("$supervisor" <<< '{"command":"host"}' 2>/dev/null)" || return 1
  python3 - "$response" <<'PY'
import json
import sys

try:
    body = json.loads(sys.argv[1])
except Exception:
    raise SystemExit(1)
host = body.get("host") or {}
if body.get("ok") is not True or host.get("backend") != "apple-vf":
    raise SystemExit(1)
if host.get("virtualizationSupported") is not True:
    raise SystemExit(1)
PY
}

# e2e_have_vm: can this host boot a microVM via the native backend?
e2e_have_vm() {
  case "$(uname -s)" in
    Linux)
      case "$(uname -m)" in
        x86_64|amd64) ;;
        *) return 1 ;;
      esac
      e2e_have_kvm || return 1
      e2e_resolve_firecracker >/dev/null 2>&1 || return 1
      return 0
      ;;
    Darwin)
      e2e_have_applevf
      ;;
    *)
      return 1
      ;;
  esac
}

e2e_require_vm() { e2e_have_vm || e2e_skip "no microVM backend available (need /dev/kvm + a microagent install that bundles firecracker, or MICROAGENT_FIRECRACKER, on Linux amd64; or macOS on Apple silicon)"; }
e2e_require_linux() { [ "$(uname -s)" = "Linux" ] || e2e_skip "Linux-only scenario"; }

# e2e_wait_exec_ready <cli> <state-dir> <ws> [timeout-sec]: poll status until the
# guest exec service answers, so exec calls don't race the boot. Readiness is
# probed on demand at each status call.
e2e_wait_exec_ready() {
  cli="$1"; sd="$2"; ws="$3"; timeout="${4:-${MICROAGENT_E2E_WAIT_TIMEOUT:-60}}"; i=0
  while [ "$i" -lt "$timeout" ]; do
    if "$cli" --json status "$ws" --state-dir "$sd" 2>/dev/null | grep -A2 '"execReady"' | grep -q '"ready": true'; then
      return 0
    fi
    sleep 1
    i=$((i + 1))
  done
  return 1
}

# e2e_wait_state <cli> <state-dir> <ws> <wanted-state> [timeout]: poll status
# until .event.state equals the wanted state.
e2e_wait_state() {
  cli="$1"; sd="$2"; ws="$3"; want="$4"; timeout="${5:-${MICROAGENT_E2E_WAIT_TIMEOUT:-90}}"; i=0
  while [ "$i" -lt "$timeout" ]; do
    if "$cli" --json status "$ws" --state-dir "$sd" 2>/dev/null \
         | python3 -c 'import sys,json;d=json.load(sys.stdin);sys.exit(0 if (d.get("event") or {}).get("state")==sys.argv[1] else 1)' "$want" 2>/dev/null; then
      return 0
    fi
    sleep 1; i=$((i + 1))
  done
  return 1
}

# e2e_wait_terminal <cli> <state-dir> <ws> [timeout]: poll status until
# .event.state is any of stopped, failed, or halted.
e2e_wait_terminal() {
  cli="$1"; sd="$2"; ws="$3"; timeout="${4:-${MICROAGENT_E2E_WAIT_TIMEOUT:-90}}"; i=0
  while [ "$i" -lt "$timeout" ]; do
    if "$cli" --json status "$ws" --state-dir "$sd" 2>/dev/null \
         | python3 -c 'import sys,json;d=json.load(sys.stdin);sys.exit(0 if (d.get("event") or {}).get("state") in ("stopped","failed","halted") else 1)' 2>/dev/null; then
      return 0
    fi
    sleep 1; i=$((i + 1))
  done
  return 1
}

# e2e_wait_host_port <host:port> [timeout]: poll until a TCP connect succeeds.
e2e_wait_host_port() {
  addr="$1"; timeout="${2:-${MICROAGENT_E2E_WAIT_TIMEOUT:-90}}"; i=0
  host="${addr%:*}"; port="${addr##*:}"
  while [ "$i" -lt "$timeout" ]; do
    if python3 -c "import socket; s=socket.socket(); s.settimeout(1); s.connect(('$host',$port)); s.close()" 2>/dev/null; then
      return 0
    fi
    sleep 1; i=$((i + 1))
  done
  return 1
}

# e2e_build_cli <out>: build the microagent CLI for the host.
e2e_build_cli() { go build -buildvcs=false -o "$1" ./cmd/microagent; }

# e2e_build_firecracker_stack <cli> <supervisor> <guestinit>: build the Linux
# firecracker binaries and export MICROAGENT_FIRECRACKER for child commands.
e2e_build_firecracker_stack() {
  go build -buildvcs=false -o "$1" ./cmd/microagent
  go build -buildvcs=false -o "$2" ./cmd/microagent-firecracker-supervisor
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$3" ./cmd/microagent-guestinit
  MICROAGENT_FIRECRACKER="$(e2e_resolve_firecracker)"
  export MICROAGENT_FIRECRACKER
}
