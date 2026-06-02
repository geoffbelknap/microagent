#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

# supervise --install writes an OS boot unit so a workspace survives host reboot.
# We validate unit generation/removal (Linux systemd user unit / macOS launchd
# plist) against a hermetic HOME; no real reboot is performed.
e2e_require_vm
e2e_require_cmd mke2fs "mke2fs is required to build the workspace rootfs"

STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-survive-reboot.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
HOME_DIR="$STATE_DIR/home"
IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox:1.36}"
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
e2e_build_firecracker_stack "$CLI" "$SUPERVISOR" "$GUEST_INIT"
"$CLI" kernel install --backend firecracker --arch amd64 >"$STATE_DIR/kernel-install.json" 2>/dev/null || e2e_fail "kernel install"
kernel_path="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["path"])' "$STATE_DIR/kernel-install.json")"

case "$(uname -s)" in
  Linux)  UNIT="$HOME_DIR/.config/systemd/user/microagent-supervise-$WS.service" ;;
  Darwin) UNIT="$HOME_DIR/Library/LaunchAgents/com.microagent.supervise.$WS.plist" ;;
  *) e2e_skip "survive-reboot units are linux/darwin only" ;;
esac

e2e_step "prepare a workspace to supervise"
"$CLI" create "$WS" --image "$IMAGE" --network isolated --service-command "sleep 600" \
  --kernel "$kernel_path" --guest-init "$GUEST_INIT" --supervisor "$SUPERVISOR" \
  --state-dir "$STATE_DIR" --size-mib 128 --result-port 0 >/dev/null 2>&1 || e2e_fail "create workspace"

e2e_step "supervise --install writes the boot unit"
HOME="$HOME_DIR" "$CLI" supervise "$WS" --install --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/install.json" 2>&1 \
  || { cat "$STATE_DIR/install.json"; e2e_fail "supervise --install"; }
[ -f "$UNIT" ] || e2e_fail "boot unit not written at $UNIT"
grep -q "supervise $WS" "$UNIT" || e2e_fail "boot unit missing supervise command for $WS"
e2e_log "unit written: $UNIT"

e2e_step "supervise --uninstall removes the boot unit"
HOME="$HOME_DIR" "$CLI" supervise "$WS" --uninstall --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 \
  || e2e_fail "supervise --uninstall"
[ ! -f "$UNIT" ] || e2e_fail "boot unit still present after uninstall"

e2e_log "survive-reboot scenario passed"
