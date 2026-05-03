#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SUPERVISOR="${MICROAGENT_APPLEVF_SUPERVISOR:-$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor}"
KERNEL="${MICROAGENT_APPLEVF_KERNEL:-$HOME/.microagent/kernels/apple-vf/arm64/Image}"
if [ ! -r "$KERNEL" ] && [ -r "$HOME/.microagent/kernels/apple-vf/Image" ]; then
  KERNEL="$HOME/.microagent/kernels/apple-vf/Image"
fi
IMAGE="${MICROAGENT_APPLEVF_BOOT_IMAGE:-docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6}"
ARCH="${MICROAGENT_APPLEVF_BOOT_ARCH:-arm64}"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-connect-smoke.XXXXXX")"
WORKSPACE="connect-smoke"
GUEST_INIT="$STATE_DIR/microagent-guestinit"
CREATE_RESULT="$STATE_DIR/create.json"
START_RESULT="$STATE_DIR/start.json"
CONNECT_RESULT="$STATE_DIR/connect.txt"

cleanup() {
  status="$?"
  "$STATE_DIR/microagent" stop "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_CONNECT_SMOKE:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept connect smoke state at $STATE_DIR"
  fi
}
trap cleanup EXIT

if [ "$(uname -s)" != "Darwin" ] || [ "$(uname -m)" != "arm64" ]; then
  echo "Apple VF connect smoke requires macOS on Apple silicon" >&2
  exit 2
fi
if [ ! -r "$KERNEL" ]; then
  echo "kernel is not readable at $KERNEL" >&2
  exit 2
fi
if [ ! -x "$SUPERVISOR" ]; then
  echo "supervisor is not executable at $SUPERVISOR; run make signed-supervisor" >&2
  exit 2
fi

if command -v mke2fs >/dev/null 2>&1; then
  MKE2FS="$(command -v mke2fs)"
elif [ -x /opt/homebrew/opt/e2fsprogs/sbin/mke2fs ]; then
  MKE2FS="/opt/homebrew/opt/e2fsprogs/sbin/mke2fs"
else
  echo "mke2fs not found; install e2fsprogs" >&2
  exit 2
fi

(
  cd "$ROOT"
  go build -o "$STATE_DIR/microagent" ./cmd/microagent
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

"$STATE_DIR/microagent" create \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --size-mib "${MICROAGENT_APPLEVF_BOOT_SIZE_MIB:-128}" \
  --mke2fs "$MKE2FS" \
  --setup "mkdir -p /workspace" \
  --setup "echo SETUP_READY > /workspace/status" \
  --name "$WORKSPACE" \
  --kernel "$KERNEL" \
  --state-dir "$STATE_DIR" \
  --memory "${MICROAGENT_APPLEVF_BOOT_MEMORY_MIB:-512}" \
  --cpus "${MICROAGENT_APPLEVF_BOOT_CPUS:-2}" \
  --timeout "${MICROAGENT_APPLEVF_BOOT_TIMEOUT_SECONDS:-30}" \
  --guest-init "$GUEST_INIT" \
  --supervisor "$SUPERVISOR" >"$CREATE_RESULT"

"$STATE_DIR/microagent" start "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" >"$START_RESULT"

for _ in $(seq 1 50); do
  if [ -p "$STATE_DIR/$WORKSPACE/serial.in" ]; then
    break
  fi
  sleep 0.1
done
for _ in $(seq 1 100); do
  if grep -q "~ #" "$STATE_DIR/$WORKSPACE/serial.log"; then
    break
  fi
  sleep 0.1
done

"$STATE_DIR/microagent" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "echo CONNECT_READY; poweroff -f" \
  --timeout 10 >"$CONNECT_RESULT"

for _ in $(seq 1 50); do
  if "$STATE_DIR/microagent" status --name "$WORKSPACE" --state-dir "$STATE_DIR" | grep -q '"state" : "stopped"'; then
    break
  fi
  sleep 0.2
done

python3 - "$CREATE_RESULT" "$START_RESULT" "$CONNECT_RESULT" "$STATE_DIR/$WORKSPACE/serial.log" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    create = json.load(f)
with open(sys.argv[2], "r", encoding="utf-8") as f:
    start = json.load(f)
with open(sys.argv[3], "r", encoding="utf-8") as f:
    connect = f.read()
with open(sys.argv[4], "r", encoding="utf-8", errors="replace") as f:
    serial = f.read()

if create.get("final_state") != "stopped":
    raise SystemExit(f"create did not stop cleanly: {create.get('final_state')}")
if start.get("response", {}).get("event", {}).get("state") != "running":
    raise SystemExit("start did not return running state")
if "CONNECT_READY" not in connect + serial:
    raise SystemExit("connect output did not reach the guest shell")
PY

"$STATE_DIR/microagent" ps --state-dir "$STATE_DIR"
"$STATE_DIR/microagent" logs "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null

echo "Apple VF workspace connect smoke passed"
