#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

# supervise --install writes an OS boot unit so a workspace survives host reboot.
# We validate unit generation/removal (Linux systemd user unit / macOS launchd
# plist) against a hermetic HOME; no real reboot is performed.
e2e_require_vm
e2e_require_cmd mke2fs "mke2fs is required to build the workspace rootfs"

default_backend() {
  case "$(uname -s):$(uname -m)" in
    Linux:x86_64|Linux:amd64)
      printf '%s\n' firecracker
      ;;
    Darwin:arm64)
      printf '%s\n' applevf
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
  firecracker)
    SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
    GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
    IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox:1.36}"
    e2e_build_firecracker_stack "$CLI" "$SUPERVISOR" "$GUEST_INIT"
    "$CLI" kernel install --backend firecracker --arch amd64 >"$STATE_DIR/kernel-install.json" 2>/dev/null || e2e_fail "kernel install"
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
  *)
    e2e_skip "survive-reboot E2E does not support backend lane: $BACKEND"
    ;;
esac

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
  *) e2e_skip "survive-reboot units are linux/darwin only" ;;
esac

e2e_step "prepare a workspace to supervise"
"$CLI" create "$WS" --image "$IMAGE" --network isolated --service-command "sleep 600" \
  "${CREATE_FLAGS[@]}" >/dev/null 2>&1 || e2e_fail "create workspace"

e2e_step "supervise --install writes the boot unit"
HOME="$HOME_DIR" "$CLI" supervise "$WS" --install --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/install.json" 2>&1 \
  || { cat "$STATE_DIR/install.json"; e2e_fail "supervise --install"; }
[ -f "$UNIT" ] || e2e_fail "boot unit not written at $UNIT"
assert_unit_command || e2e_fail "boot unit missing supervise command for $WS"
e2e_log "unit written: $UNIT"

e2e_step "supervise --uninstall removes the boot unit"
HOME="$HOME_DIR" "$CLI" supervise "$WS" --uninstall --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 \
  || e2e_fail "supervise --uninstall"
[ ! -f "$UNIT" ] || e2e_fail "boot unit still present after uninstall"

e2e_log "survive-reboot scenario passed"
