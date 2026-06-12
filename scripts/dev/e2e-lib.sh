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

# e2e_is_windows: true under Git Bash / MSYS / Cygwin on a Windows host
# (NOT WSL, which reports Linux and runs the Linux lane).
e2e_is_windows() {
  case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) return 0 ;;
  esac
  return 1
}

# e2e_friendly_os: short OS name for preflight output.
e2e_friendly_os() {
  if e2e_is_windows; then
    printf '%s\n' Windows
  else
    uname -s
  fi
}

# e2e_exe <path>: append .exe on Windows so built binaries resolve when
# spawned by the CLI itself (os.Executable-based helpers).
e2e_exe() {
  if e2e_is_windows; then
    printf '%s.exe\n' "$1"
  else
    printf '%s\n' "$1"
  fi
}

# e2e_host_path <path>: print the host-native form of a path for argument
# values the CLI parses itself (volume/disk specs). Git Bash converts plain
# path arguments automatically but mangles colon-separated specs, so
# scenarios pass Windows-form paths inside such specs.
e2e_host_path() {
  if e2e_is_windows && command -v cygpath >/dev/null 2>&1; then
    cygpath -m "$1"
  else
    printf '%s\n' "$1"
  fi
}

# e2e_host_pid: print the host-native PID of this shell (the Windows PID
# under Git Bash, where $$ lives in the MSYS pid namespace).
e2e_host_pid() {
  if e2e_is_windows && [ -r "/proc/$$/winpid" ]; then
    cat "/proc/$$/winpid"
  else
    printf '%s
' "$$"
  fi
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

# e2e_have_hcs: Windows Host Compute Service stack is running (vmms +
# vmcompute), which is what the windows-hyperv backend needs to boot.
e2e_have_hcs() {
  e2e_is_windows || return 1
  command -v sc >/dev/null 2>&1 || return 1
  sc query vmcompute 2>/dev/null | grep -q 'RUNNING' || return 1
  sc query vmms 2>/dev/null | grep -q 'RUNNING'
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
      [ "$(uname -m)" = "arm64" ]
      ;;
    MINGW*|MSYS*|CYGWIN*)
      case "$(uname -m)" in
        x86_64|amd64) ;;
        *) return 1 ;;
      esac
      e2e_have_hcs
      ;;
    *)
      return 1
      ;;
  esac
}

# e2e_have_ip_forward: IPv4 forwarding enabled (or we are root and can enable it).
e2e_have_ip_forward() {
  if [ "$(cat /proc/sys/net/ipv4/ip_forward 2>/dev/null || echo 0)" = "1" ]; then
    return 0
  fi
  e2e_is_root
}

# e2e_have_netpriv: can this host run privileged TAP/bridge networking?
# Linux only: needs a VM, the ability to gain CAP_NET_ADMIN (root, or a
# pre-granted capability set), and IPv4 forwarding.
e2e_have_netpriv() {
  [ "$(uname -s)" = "Linux" ] || return 1
  e2e_have_vm || return 1
  e2e_have_ip_forward || return 1
  # Operator override: set when you hold CAP_NET_ADMIN without uid 0 (file caps,
  # a capability-granting sudo/namespace, or a privileged CI runner).
  if [ "${MICROAGENT_E2E_ALLOW_NETPRIV:-0}" = "1" ]; then
    return 0
  fi
  if e2e_is_root; then
    return 0
  fi
  # Non-root: only if CAP_NET_ADMIN is in the EFFECTIVE set (the "Current:" line),
  # not merely the bounding set. The supervisor needs it effective to pass to the VM.
  if command -v capsh >/dev/null 2>&1 && capsh --print 2>/dev/null | grep '^Current:' | grep -q 'cap_net_admin'; then
    return 0
  fi
  return 1
}

e2e_require_vm() { e2e_have_vm || e2e_skip "no microVM backend available (need /dev/kvm + a microagent install that bundles firecracker, or MICROAGENT_FIRECRACKER, on Linux amd64; or macOS on Apple silicon)"; }
e2e_require_netpriv() { e2e_have_netpriv || e2e_skip "privileged networking unavailable (need root/CAP_NET_ADMIN + net.ipv4.ip_forward=1 on Linux)"; }
e2e_require_linux() { [ "$(uname -s)" = "Linux" ] || e2e_skip "Linux-only scenario"; }

# e2e_wait_exec_ready <cli> <state-dir> <ws> [timeout-sec]: poll status until the
# guest exec service answers, so exec calls don't race the boot. Readiness is
# probed on demand at each status call.
e2e_wait_exec_ready() {
  cli="$1"; sd="$2"; ws="$3"; timeout="${4:-60}"; i=0
  while [ "$i" -lt "$timeout" ]; do
    if "$cli" --json status "$ws" --state-dir "$sd" 2>/dev/null | grep -A2 '"execReady"' | grep -q '"ready": true'; then
      return 0
    fi
    sleep 1
    i=$((i + 1))
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
