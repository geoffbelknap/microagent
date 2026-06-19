#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

# supervise --install writes an OS boot unit so a workspace survives host reboot.
# We validate unit generation/removal (Linux systemd user unit / macOS launchd
# plist) against a hermetic HOME; no real reboot is performed.
e2e_require_vm
# mke2fs is an ext4-lane prerequisite; the Windows VHD builder needs none.
if ! e2e_is_windows; then
  e2e_require_cmd mke2fs "mke2fs is required to build the workspace rootfs"
fi

default_backend() {
  case "$(uname -s):$(uname -m)" in
    Linux:x86_64|Linux:amd64)
      printf '%s\n' linux-kvm
      ;;
    Darwin:arm64)
      printf '%s\n' applevf
      ;;
    MINGW*:x86_64|MSYS*:x86_64|CYGWIN*:x86_64)
      printf '%s\n' windows-hyperv
      ;;
    *)
      printf '%s\n' unsupported
      ;;
  esac
}

BACKEND="${MICROAGENT_E2E_BACKEND:-$(default_backend)}"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-survive-reboot.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR=""
HOME_DIR="$STATE_DIR/home"
WS="reboot-survivor"
mkdir -p "$HOME_DIR"

cleanup() {
  status="$?"
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_SURVIVE_REBOOT:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E survive-reboot state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

cd "$ROOT"
export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
if [ -z "${DOCKER_CONFIG:-}" ]; then
  mkdir -p "$STATE_DIR/docker-config"
  export DOCKER_CONFIG="$STATE_DIR/docker-config"
fi

case "$BACKEND" in
  linux-kvm)
    SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
    GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
    IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox:1.36}"
    e2e_build_firecracker_stack "$CLI" "$SUPERVISOR" "$GUEST_INIT"
    "$CLI" kernel install --backend linux-kvm --arch amd64 >"$STATE_DIR/kernel-install.json" 2>/dev/null || e2e_fail "kernel install"
    KERNEL="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["path"])' "$STATE_DIR/kernel-install.json")"
    CREATE_FLAGS=(--kernel "$KERNEL" --guest-init "$GUEST_INIT" --supervisor "$SUPERVISOR" --state-dir "$STATE_DIR" --size-mib 128 --result-port 0)
    ;;
  applevf)
    case "$(uname -s):$(uname -m)" in
      Darwin:arm64)
        ;;
      *)
        e2e_skip "Apple VF survive-reboot E2E requires macOS on Apple silicon"
        ;;
    esac
    SUPERVISOR="${MICROAGENT_APPLEVF_SUPERVISOR:-$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor}"
    KERNEL="${MICROAGENT_APPLEVF_KERNEL:-$HOME/.microagent/kernels/apple-vf/arm64/Image}"
    if [ ! -r "$KERNEL" ] && [ -r "$HOME/.microagent/kernels/apple-vf/Image" ]; then
      KERNEL="$HOME/.microagent/kernels/apple-vf/Image"
    fi
    if [ ! -x "$SUPERVISOR" ]; then
      e2e_skip "supervisor is not executable at $SUPERVISOR; run scripts/dev/applevf-supervisor-build.sh"
    fi
    if [ ! -r "$KERNEL" ]; then
      e2e_skip "kernel is not readable at $KERNEL"
    fi
    ARCH="${MICROAGENT_APPLEVF_BOOT_ARCH:-arm64}"
    IMAGE="${MICROAGENT_APPLEVF_BOOT_IMAGE:-docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6}"
    GUEST_INIT="$STATE_DIR/microagent-guestinit"
    go build -buildvcs=false -o "$CLI" ./cmd/microagent
    GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
    CREATE_FLAGS=(--backend apple-vf --kernel "$KERNEL" --guest-init "$GUEST_INIT" --supervisor "$SUPERVISOR" --state-dir "$STATE_DIR" --size-mib 128 --result-port 0)
    ;;
  windows-hyperv)
    e2e_have_hcs || e2e_skip "Hyper-V HCS services (vmms/vmcompute) are not running"
    IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox:1.36}"
    KERNEL="$HOME/.microagent/kernels/windows-hyperv/amd64/Image"
    GUEST_INIT="$STATE_DIR/microagent-guestinit"
    CLI="$STATE_DIR/microagent.exe"
    go build -buildvcs=false -o "$CLI" ./cmd/microagent
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
    if [ ! -r "$KERNEL" ]; then
      "$CLI" kernel install || e2e_skip "windows-hyperv kernel install failed"
    fi
    CREATE_FLAGS=(--kernel "$KERNEL" --guest-init "$GUEST_INIT" --state-dir "$STATE_DIR" --size-mib 512 --result-port 0)
    ;;
  *)
    e2e_skip "survive-reboot E2E does not support backend lane: $BACKEND"
    ;;
esac

# The windows-hyperv supervisor runs in-process; only the executable
# supervisors take a --supervisor path.
SUPERVISOR_FLAGS=()
if [ -n "$SUPERVISOR" ]; then
  SUPERVISOR_FLAGS=(--supervisor "$SUPERVISOR")
fi

case "$(uname -s)" in
  Linux)
    UNIT="$HOME_DIR/.config/systemd/user/microagent-supervise-$WS.service"
    assert_unit_command() { grep -q "supervise $WS" "$UNIT"; }
    ;;
  Darwin)
    UNIT="$HOME_DIR/Library/LaunchAgents/com.microagent.supervise.$WS.plist"
    assert_unit_command() {
      grep -q "<string>supervise</string>" "$UNIT" && grep -q "<string>$WS</string>" "$UNIT"
    }
    ;;
  MINGW*|MSYS*|CYGWIN*)
    UNIT="$HOME_DIR/.microagent/tasks/microagent-supervise-$WS.xml"
    assert_unit_command() {
      grep -q "<Arguments>supervise $WS" "$UNIT" && grep -q "<LogonTrigger>" "$UNIT"
    }
    # On Windows the boot unit is a Scheduled Task XML registered with
    # `schtasks /Create`, which needs an elevated token. Both outcomes are
    # valid and both are asserted honestly:
    #   - elevated host (hosted CI runners run elevated): registration
    #     succeeds, enabled=true, and no manual command is needed — the
    #     uninstall below proves the /Delete round-trip.
    #   - unelevated host (this dev shell): `/Create` returns "Access is
    #     denied", enabled=false, and the install must surface the manual
    #     `schtasks /Create /TN <label> /XML <file> /F` command alongside the
    #     written unit file (fail-open contract).
    assert_install_json() {
      python3 - "$STATE_DIR/install.json" "$UNIT" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    data = json.load(handle)
label = "microagent-supervise-reboot-survivor"
if data.get("installed") != label:
    raise SystemExit(f"install label = {data.get('installed')!r}, want {label!r}")
enabled = data.get("enabled")
if enabled is True:
    # Elevated host: the registration round-trip succeeded; no manual
    # command is expected in the JSON.
    raise SystemExit(0)
if enabled is not False:
    raise SystemExit(f"unexpected enabled value: {enabled!r}")
# Unelevated host: the denied registration must surface the manual command.
enable = data.get("enable_command", "")
for token in ("schtasks", "/Create", "/TN", label, "/XML", "/F"):
    if token not in enable:
        raise SystemExit(f"enable_command missing {token!r}: {enable!r}")
if not data.get("enable_error"):
    raise SystemExit(f"enabled=false without enable_error: {data!r}")
PY
    }
    ;;
  *) e2e_skip "survive-reboot units are linux/darwin/windows only" ;;
esac

# Go resolves the home directory from USERPROFILE on Windows; point both at
# the scenario's temp home so the unit lands where the assertions look.
UNIT_HOME_ENV=(HOME="$HOME_DIR")
if e2e_is_windows; then
  UNIT_HOME_ENV+=(USERPROFILE="$(e2e_host_path "$HOME_DIR")")
fi

e2e_step "prepare a workspace to supervise"
"$CLI" create "$WS" --image "$IMAGE" --network isolated --service-command "sleep 600" \
  "${CREATE_FLAGS[@]}" >/dev/null 2>&1 || e2e_fail "create workspace"

e2e_step "supervise --install writes the boot unit"
env "${UNIT_HOME_ENV[@]}" "$CLI" --json supervise "$WS" --install --state-dir "$STATE_DIR" "${SUPERVISOR_FLAGS[@]}" >"$STATE_DIR/install.json" 2>"$STATE_DIR/install.err" \
  || { cat "$STATE_DIR/install.json" "$STATE_DIR/install.err"; e2e_fail "supervise --install"; }
[ -f "$UNIT" ] || e2e_fail "boot unit not written at $UNIT"
assert_unit_command || e2e_fail "boot unit missing supervise command for $WS"
# On Windows, also assert the schtasks enable command shape and honest gating.
if e2e_is_windows; then
  assert_install_json || e2e_fail "supervise --install JSON did not report the expected schtasks enable contract"
fi
e2e_log "unit written: $UNIT"

e2e_step "supervise --uninstall removes the boot unit"
env "${UNIT_HOME_ENV[@]}" "$CLI" supervise "$WS" --uninstall --state-dir "$STATE_DIR" "${SUPERVISOR_FLAGS[@]}" >/dev/null 2>&1 \
  || e2e_fail "supervise --uninstall"
[ ! -f "$UNIT" ] || e2e_fail "boot unit still present after uninstall"

e2e_log "survive-reboot scenario passed"
