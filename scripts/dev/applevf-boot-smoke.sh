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
NETWORK_MODE="${MICROAGENT_APPLEVF_BOOT_NETWORK_MODE:-user}"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-applevf-boot.XXXXXX")"
RESULT="$STATE_DIR/result.json"
GUEST_INIT="$STATE_DIR/microagent-guestinit"

cleanup() {
  if [ "${MICROAGENT_KEEP_BOOT_SMOKE:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept boot smoke state at $STATE_DIR"
  fi
}
trap cleanup EXIT

if [ "$(uname -s)" != "Darwin" ] || [ "$(uname -m)" != "arm64" ]; then
  e2e_skip "Apple VF boot smoke requires macOS on Apple silicon"
fi
if [ ! -r "$KERNEL" ]; then
  echo "kernel is not readable at $KERNEL" >&2
  e2e_skip "download or build a Linux ARM64 kernel Image before running this smoke"
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
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

RUN_ARGS=(
  run
  --image "$IMAGE"
  --arch "$ARCH"
  --size-mib "${MICROAGENT_APPLEVF_BOOT_SIZE_MIB:-128}"
  --mke2fs "$MKE2FS"
  --exec "echo MICROAGENT_BOOT_OK; uname -m"
  --name boot-smoke
  --kernel "$KERNEL"
  --state-dir "$STATE_DIR"
  --memory "${MICROAGENT_APPLEVF_BOOT_MEMORY_MIB:-512}"
  --cpus "${MICROAGENT_APPLEVF_BOOT_CPUS:-2}"
  --network "$NETWORK_MODE"
  --timeout "${MICROAGENT_APPLEVF_BOOT_TIMEOUT_SECONDS:-30}"
  --guest-init "$GUEST_INIT"
  --supervisor "$SUPERVISOR"
)

"$STATE_DIR/microagent" "${RUN_ARGS[@]}" >"$RESULT"

python3 - "$RESULT" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)

serial = result.get("serial_log", "")
print(serial[-8000:])
guest = result.get("result") or {}
stdout = guest.get("stdout", "")
if "MICROAGENT_BOOT_OK" not in stdout:
    raise SystemExit("VM booted but command output was not found in guest result")
if guest.get("exit_code") != 0:
    raise SystemExit(f"unexpected exit code: {guest.get('exit_code')}")
if result.get("final_state") != "stopped":
    raise SystemExit(f"unexpected final state: {result.get('final_state')}")
PY

echo "Apple VF boot smoke reached stopped state"
