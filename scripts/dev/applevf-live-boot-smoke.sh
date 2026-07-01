#!/usr/bin/env bash
# Real Apple VF boot-to-userspace smoke. Unlike cli-workspace-smoke.sh (stub
# kernel + fake supervisor), this boots a real guest under the *mandatory*
# Seatbelt VMM-process confinement and proves the guest reached userspace by
# asserting the in-guest `echo` marker and its exit code came back over vsock.
# This is the parity gate for the macOS confinement default: if the confinement
# profile is too tight, the VM fails closed and no marker is ever produced.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

SUPERVISOR="${MICROAGENT_APPLEVF_SUPERVISOR:-$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor}"
KERNEL="${MICROAGENT_APPLEVF_KERNEL:-$HOME/.microagent/kernels/apple-vf/arm64/Image}"
if [ ! -r "$KERNEL" ] && [ -r "$HOME/.microagent/kernels/apple-vf/Image" ]; then
  KERNEL="$HOME/.microagent/kernels/apple-vf/Image"
fi
IMAGE="${MICROAGENT_APPLEVF_BOOT_IMAGE:-docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6}"
ARCH="${MICROAGENT_APPLEVF_BOOT_ARCH:-arm64}"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-applevf-live-boot.XXXXXX")"
WORKSPACE="live-boot-smoke"
CLI="$STATE_DIR/microagent"
GUEST_INIT="$STATE_DIR/microagent-guestinit"
RESULT="$STATE_DIR/result.json"
HOST="$STATE_DIR/host.json"
MARKER="microagent-live-boot-$$-${RANDOM}"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    "$CLI" stop "$WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
  fi
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_APPLEVF_LIVE_BOOT_SMOKE:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept Apple VF live-boot smoke state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

if [ "$(uname -s)" != "Darwin" ] || [ "$(uname -m)" != "arm64" ]; then
  e2e_skip "Apple VF live-boot smoke requires macOS on Apple silicon"
fi
if [ ! -r "$KERNEL" ]; then
  e2e_skip "kernel is not readable at $KERNEL; run microagent kernel install"
fi
if [ ! -x "$SUPERVISOR" ]; then
  e2e_skip "supervisor is not executable at $SUPERVISOR; run make signed-supervisor"
fi
if command -v mke2fs >/dev/null 2>&1; then
  MKE2FS="$(command -v mke2fs)"
elif [ -x /opt/homebrew/opt/e2fsprogs/sbin/mke2fs ]; then
  MKE2FS="/opt/homebrew/opt/e2fsprogs/sbin/mke2fs"
else
  e2e_skip "mke2fs not found; install e2fsprogs"
fi

(
  cd "$ROOT"
  go build -o "$CLI" ./cmd/microagent
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

# Confinement must actually be active on this host before we trust a green boot:
# a passing run with confinement silently off would not prove parity.
"$SUPERVISOR" <<< '{"command":"host"}' >"$HOST"

# One-shot boot under the mandatory Seatbelt profile: boot the real guest, run
# the marker echo in userspace, capture its result over vsock, then tear down.
"$CLI" run \
  --backend apple-vf \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --exec "echo $MARKER" \
  --name "$WORKSPACE" \
  --kernel "$KERNEL" \
  --guest-init "$GUEST_INIT" \
  --supervisor "$SUPERVISOR" \
  --state-dir "$STATE_DIR" \
  --size-mib "${MICROAGENT_APPLEVF_BOOT_SIZE_MIB:-128}" \
  --mke2fs "$MKE2FS" \
  --egress off >"$RESULT"

python3 - "$RESULT" "$HOST" "$MARKER" "$WORKSPACE" <<'PY'
import json
import sys

result_path, host_path, marker, workspace = sys.argv[1:5]

with open(host_path, "r", encoding="utf-8") as f:
    host = json.load(f)
hs = host.get("host", host)
if not hs.get("confinementActive"):
    raise SystemExit(f"confinement is not active on this host: {host}")
if hs.get("confinementMode") != "seatbelt":
    raise SystemExit(f"unexpected confinement mode: {host}")

with open(result_path, "r", encoding="utf-8") as f:
    result = json.load(f)

if result.get("workspace") != workspace:
    raise SystemExit(f"workspace = {result.get('workspace')!r}")
if result.get("final_state") != "stopped":
    raise SystemExit(f"final_state = {result.get('final_state')!r}: {result.get('response')}")

guest = result.get("result")
if not guest:
    raise SystemExit(f"no guest result captured (guest never reached userspace): {result}")
if guest.get("exit_code") != 0:
    raise SystemExit(f"guest exit_code = {guest.get('exit_code')!r}, stderr={guest.get('stderr')!r}")
if marker not in (guest.get("stdout") or ""):
    raise SystemExit(f"marker {marker!r} not in guest stdout {guest.get('stdout')!r}")

print(f"guest booted to userspace under {hs.get('confinementMode')} confinement; marker echoed, exit 0")
PY

echo "Apple VF live-boot smoke passed"
