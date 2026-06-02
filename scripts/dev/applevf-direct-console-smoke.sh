#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"
SUPERVISOR="${MICROAGENT_APPLEVF_SUPERVISOR:-$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor}"
KERNEL="${MICROAGENT_APPLEVF_KERNEL:-$HOME/.microagent/kernels/apple-vf/arm64/Image}"
if [ ! -r "$KERNEL" ] && [ -r "$HOME/.microagent/kernels/apple-vf/Image" ]; then
  KERNEL="$HOME/.microagent/kernels/apple-vf/Image"
fi
IMAGE="${MICROAGENT_APPLEVF_BOOT_IMAGE:-docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6}"
ARCH="${MICROAGENT_APPLEVF_BOOT_ARCH:-arm64}"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-direct-console.XXXXXX")"
ROOTFS="$STATE_DIR/rootfs.ext4"
REQUEST="$STATE_DIR/request.json"
OUTPUT="$STATE_DIR/console.txt"
INPUT="$STATE_DIR/console.in"

cleanup() {
  if [ "${MICROAGENT_KEEP_DIRECT_CONSOLE_SMOKE:-0}" != "1" ]; then
    sleep 1
    rm -rf "$STATE_DIR" || true
  else
    echo "kept direct console smoke state at $STATE_DIR"
  fi
}
trap cleanup EXIT

if [ "$(uname -s)" != "Darwin" ] || [ "$(uname -m)" != "arm64" ]; then
  e2e_skip "Apple VF direct console smoke requires macOS on Apple silicon"
fi
if [ ! -r "$KERNEL" ]; then
  e2e_skip "kernel is not readable at $KERNEL"
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
  go build -o "$STATE_DIR/microagent" ./cmd/microagent
)

"$STATE_DIR/microagent" rootfs build \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --out "$ROOTFS" \
  --size-mib "${MICROAGENT_APPLEVF_BOOT_SIZE_MIB:-128}" \
  --mke2fs "$MKE2FS" \
  --state-dir "$STATE_DIR/build" >/dev/null

python3 - "$REQUEST" "$KERNEL" "$ROOTFS" "$STATE_DIR" <<'PY'
import json
import sys

request_path, kernel_path, rootfs_path, state_dir = sys.argv[1:]
request = {
    "command": "console",
    "identity": {
        "requestID": "direct-console-smoke",
        "runtimeID": "direct-console-smoke",
        "role": "workload",
        "backend": "apple-vf",
    },
    "config": {
        "kernelPath": kernel_path,
        "rootfsPath": rootfs_path,
        "stateDir": state_dir,
        "memoryMiB": 512,
        "cpuCount": 2,
    },
}
with open(request_path, "w", encoding="utf-8") as f:
    json.dump(request, f)
PY

mkfifo "$INPUT"
"$SUPERVISOR" --request "$REQUEST" <"$INPUT" >"$OUTPUT" &
SUPERVISOR_PID="$!"

exec 3>"$INPUT"
sleep "${MICROAGENT_DIRECT_CONSOLE_INPUT_DELAY_SECONDS:-4}"
printf 'echo DIRECT_CONSOLE_READY\rpoweroff -f\r' >&3
exec 3>&-
wait "$SUPERVISOR_PID"

if ! grep -q "DIRECT_CONSOLE_READY" "$OUTPUT"; then
  echo "direct console input did not reach the guest" >&2
  tail -n 80 "$OUTPUT" >&2
  exit 1
fi

echo "Apple VF direct console smoke passed"
