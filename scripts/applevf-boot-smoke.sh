#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HELPER="${MICROAGENT_APPLEVF_HELPER:-$ROOT/helpers/applevf/.build/release/microagent-applevf-helper}"
KERNEL="${MICROAGENT_APPLEVF_KERNEL:-$HOME/.microagent/kernels/apple-vf/Image}"
IMAGE="${MICROAGENT_APPLEVF_BOOT_IMAGE:-docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6}"
ARCH="${MICROAGENT_APPLEVF_BOOT_ARCH:-arm64}"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-applevf-boot.XXXXXX")"
ROOTFS="$STATE_DIR/rootfs.ext4"

cleanup() {
  if [ "${MICROAGENT_KEEP_BOOT_SMOKE:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept boot smoke state at $STATE_DIR"
  fi
}
trap cleanup EXIT

if [ "$(uname -s)" != "Darwin" ] || [ "$(uname -m)" != "arm64" ]; then
  echo "Apple VF boot smoke requires macOS on Apple silicon" >&2
  exit 2
fi
if [ ! -r "$KERNEL" ]; then
  echo "kernel is not readable at $KERNEL" >&2
  echo "download or build a Linux ARM64 kernel Image before running this smoke" >&2
  exit 2
fi
if [ ! -x "$HELPER" ]; then
  echo "helper is not executable at $HELPER; run make signed-helper" >&2
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

go build -o "$STATE_DIR/microagent" "$ROOT/cmd/microagent"

"$STATE_DIR/microagent" rootfs build \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --size-mib "${MICROAGENT_APPLEVF_BOOT_SIZE_MIB:-128}" \
  --mke2fs "$MKE2FS" \
  --exec "echo MICROAGENT_BOOT_OK; uname -m; poweroff -f || halt -f || true" \
  --out "$ROOTFS" >/dev/null

run_cli() {
  "$STATE_DIR/microagent" "$@" --helper "$HELPER"
}

run_cli start \
  --id boot-smoke \
  --kernel "$KERNEL" \
  --rootfs "$ROOTFS" \
  --state-dir "$STATE_DIR" \
  --memory "${MICROAGENT_APPLEVF_BOOT_MEMORY_MIB:-512}" \
  --cpus "${MICROAGENT_APPLEVF_BOOT_CPUS:-2}" >/dev/null

serial="$STATE_DIR/boot-smoke/serial.log"
deadline=$((SECONDS + ${MICROAGENT_APPLEVF_BOOT_TIMEOUT_SECONDS:-30}))
state=""
while [ "$SECONDS" -lt "$deadline" ]; do
  status="$(run_cli status boot-smoke --state-dir "$STATE_DIR")"
  state="$(python3 - <<'PY' "$status"
import json
import sys

print((json.loads(sys.argv[1]).get("event") or {}).get("state", ""))
PY
)"
  if [ "$state" = "running" ] || [ "$state" = "stopped" ] || [ "$state" = "failed" ]; then
    break
  fi
  sleep 1
done

if [ "$state" != "running" ] && [ "$state" != "stopped" ]; then
  echo "VM did not reach running/stopped state; last state: ${state:-unknown}" >&2
  if [ -f "$serial" ]; then
    tail -100 "$serial" >&2
  fi
  run_cli delete boot-smoke --state-dir "$STATE_DIR" >/dev/null || true
  exit 1
fi

if [ -f "$serial" ]; then
  tail -100 "$serial"
fi

if ! grep -q "MICROAGENT_BOOT_OK" "$serial"; then
  echo "VM booted but command output was not found in serial log" >&2
  run_cli delete boot-smoke --state-dir "$STATE_DIR" >/dev/null || true
  exit 1
fi

run_cli stop boot-smoke --state-dir "$STATE_DIR" >/dev/null || true
run_cli delete boot-smoke --state-dir "$STATE_DIR" >/dev/null || true
echo "Apple VF boot smoke reached state: $state"
