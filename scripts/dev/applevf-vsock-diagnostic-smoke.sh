#!/usr/bin/env bash
# shellcheck disable=SC2016,SC2317
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SUPERVISOR="${MICROAGENT_APPLEVF_SUPERVISOR:-$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor}"
KERNEL="${MICROAGENT_APPLEVF_KERNEL:-$HOME/.microagent/kernels/apple-vf/arm64/Image}"
if [ ! -r "$KERNEL" ] && [ -r "$HOME/.microagent/kernels/apple-vf/Image" ]; then
  KERNEL="$HOME/.microagent/kernels/apple-vf/Image"
fi
IMAGE="${MICROAGENT_APPLEVF_BOOT_IMAGE:-docker.io/library/busybox:1.36}"
ARCH="${MICROAGENT_APPLEVF_BOOT_ARCH:-arm64}"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-applevf-vsock.XXXXXX")"
HOST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-applevf-vsock-host.XXXXXX")"
WORKSPACE="vsock-diagnostic"
RESULT="$STATE_DIR/result.json"
START_RESULT="$STATE_DIR/start.json"
GUEST_INIT="$STATE_DIR/microagent-guestinit"

cleanup() {
  status="$?"
  set +e
  if [ -n "${SERVER_PID:-}" ]; then
    kill "$SERVER_PID" >/dev/null 2>&1
    wait "$SERVER_PID" >/dev/null 2>&1
  fi
  "$STATE_DIR/microagent" stop "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_VSOCK_DIAGNOSTIC:-0}" != "1" ]; then
    rm -rf "$STATE_DIR" "$HOST_DIR"
  else
    echo "kept vsock diagnostic state at $STATE_DIR" >&2
    echo "kept vsock diagnostic host dir at $HOST_DIR" >&2
    [ -f "$STATE_DIR/$WORKSPACE/serial.log" ] && tail -n 240 "$STATE_DIR/$WORKSPACE/serial.log" >&2
  fi
}
trap cleanup EXIT INT TERM HUP

if [ "$(uname -s)" != "Darwin" ] || [ "$(uname -m)" != "arm64" ]; then
  echo "Apple VF vsock diagnostic requires macOS on Apple silicon" >&2
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

HOST_PORT="$(python3 - <<'PY'
import socket
with socket.socket() as s:
    s.bind(("127.0.0.1", 0))
    print(s.getsockname()[1])
PY
)"

printf 'bridge-ok\n' >"$HOST_DIR/health"
python3 -m http.server "$HOST_PORT" --bind 127.0.0.1 --directory "$HOST_DIR" >"$STATE_DIR/http.log" 2>&1 &
SERVER_PID="$!"

for _ in $(seq 1 40); do
  if curl -fsS "http://127.0.0.1:$HOST_PORT/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.25
done

(
  cd "$ROOT"
  go build -o "$STATE_DIR/microagent" ./cmd/microagent
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

diagnostic='
mkdir -p /proc /sys
mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs sysfs /sys 2>/dev/null || true
echo MICROAGENT_VSOCK_DIAGNOSTIC_START
echo "--- uname ---"
uname -a
echo "--- proc devices ---"
cat /proc/devices 2>/dev/null || true
echo "--- proc net vsock ---"
cat /proc/net/vsock 2>/dev/null || true
echo "--- virtio devices ---"
for device in /sys/bus/virtio/devices/*; do
  [ -e "$device" ] || continue
  printf "%s " "$device"
  printf "device="
  cat "$device/device" 2>/dev/null || printf "?"
  printf " vendor="
  cat "$device/vendor" 2>/dev/null || printf "?"
  printf " status="
  cat "$device/status" 2>/dev/null || printf "?"
  printf "\n"
done
echo "--- virtio drivers ---"
find /sys/bus/virtio/drivers -maxdepth 2 -type l -print 2>/dev/null || true
echo "--- pci devices ---"
for device in /sys/bus/pci/devices/*; do
  [ -e "$device" ] || continue
  printf "%s " "$device"
  printf "vendor="
  cat "$device/vendor" 2>/dev/null || printf "?"
  printf " device="
  cat "$device/device" 2>/dev/null || printf "?"
  printf " class="
  cat "$device/class" 2>/dev/null || printf "?"
  printf "\n"
done
echo "--- dmesg vsock ---"
dmesg 2>/dev/null | grep -i vsock || true
echo "--- dmesg virtio ---"
dmesg 2>/dev/null | grep -i virtio || true
echo "--- bridge probe ---"
if command -v wget >/dev/null 2>&1; then
  wget -S -O- http://127.0.0.1:3128/health || true
elif command -v curl >/dev/null 2>&1; then
  curl -v http://127.0.0.1:3128/health || true
elif command -v python3 >/dev/null 2>&1; then
  python3 - <<PY || true
import urllib.request
try:
    print(urllib.request.urlopen("http://127.0.0.1:3128/health", timeout=15).read().decode())
except Exception as exc:
    print(f"python bridge probe failed: {exc}")
PY
else
  echo "no wget, curl, or python3 available for bridge probe"
fi
sleep 12
echo MICROAGENT_VSOCK_DIAGNOSTIC_DONE
'

create_args=(
  create "$WORKSPACE"
  --image "$IMAGE"
  --arch "$ARCH"
  --size-mib "${MICROAGENT_APPLEVF_BOOT_SIZE_MIB:-128}"
  --mke2fs "$MKE2FS"
  --env MICROAGENT_VSOCK_TCP_LISTENERS=3128=3128
  --entrypoint "$diagnostic"
  --result-port 0
  --kernel "$KERNEL"
  --state-dir "$STATE_DIR"
  --memory "${MICROAGENT_APPLEVF_BOOT_MEMORY_MIB:-512}"
  --cpus "${MICROAGENT_APPLEVF_BOOT_CPUS:-2}"
  --timeout "${MICROAGENT_APPLEVF_BOOT_TIMEOUT_SECONDS:-30}"
  --guest-init "$GUEST_INIT"
  --supervisor "$SUPERVISOR"
)

"$STATE_DIR/microagent" "${create_args[@]}" >"$RESULT"

"$STATE_DIR/microagent" start "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" \
  --vsock "3128=127.0.0.1:$HOST_PORT" >"$START_RESULT"

SERIAL_PATH="$(python3 - "$START_RESULT" <<'PY'
import json
import sys
with open(sys.argv[1], "r", encoding="utf-8") as f:
    print(json.load(f).get("serial_path", ""))
PY
)"
for _ in $(seq 1 80); do
  if [ -n "$SERIAL_PATH" ] && [ -e "$SERIAL_PATH" ]; then
    break
  fi
  sleep 0.25
done
for _ in $(seq 1 120); do
  if [ -n "$SERIAL_PATH" ] && grep -q "MICROAGENT_VSOCK_DIAGNOSTIC_DONE" "$SERIAL_PATH" 2>/dev/null; then
    break
  fi
  sleep 0.25
done

"$STATE_DIR/microagent" logs "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/serial.log"
cat "$STATE_DIR/serial.log"

if grep -q "bridge-ok" "$STATE_DIR/serial.log"; then
  echo "Apple VF vsock diagnostic passed"
  exit 0
fi

if grep -q "connect vsock bridge port 3128: no such device" "$STATE_DIR/serial.log"; then
  echo "Apple VF vsock diagnostic failed: guest AF_VSOCK transport is unavailable" >&2
else
  echo "Apple VF vsock diagnostic failed: bridge probe did not reach host" >&2
fi
exit 1
