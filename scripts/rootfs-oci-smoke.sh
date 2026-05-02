#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-rootfs-smoke.XXXXXX")"
OUT="$STATE_DIR/busybox-rootfs.ext4"

case "$(uname -m)" in
  arm64|aarch64)
    ARCH="${MICROAGENT_ROOTFS_SMOKE_ARCH:-arm64}"
    IMAGE_DEFAULT="docker.io/library/busybox@sha256:bd44eb136a95dcc8dc58995e43abc40a413f2e8e3d4a2aae6bccbe94686acb05"
    ;;
  x86_64|amd64)
    ARCH="${MICROAGENT_ROOTFS_SMOKE_ARCH:-amd64}"
    IMAGE_DEFAULT="docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f"
    ;;
  *)
    ARCH="${MICROAGENT_ROOTFS_SMOKE_ARCH:-$(uname -m)}"
    IMAGE_DEFAULT="docker.io/library/busybox:1.36.1"
    ;;
esac
IMAGE="${MICROAGENT_ROOTFS_SMOKE_IMAGE:-$IMAGE_DEFAULT}"

cleanup() {
  rm -rf "$STATE_DIR"
}
trap cleanup EXIT

if command -v mke2fs >/dev/null 2>&1; then
  MKE2FS="$(command -v mke2fs)"
elif [ -x /opt/homebrew/opt/e2fsprogs/sbin/mke2fs ]; then
  MKE2FS="/opt/homebrew/opt/e2fsprogs/sbin/mke2fs"
else
  echo "mke2fs not found; install e2fsprogs to run this smoke" >&2
  exit 2
fi

go build -o "$STATE_DIR/microagent" "$ROOT/cmd/microagent"

response="$("$STATE_DIR/microagent" rootfs build \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --size-mib 64 \
  --mke2fs "$MKE2FS" \
  --out "$OUT")"

test -s "$OUT"
python3 - "$OUT" "$response" <<'PY'
import json
import os
import sys

out = sys.argv[1]
body = json.loads(sys.argv[2])
if body.get("builder") != "microagent-rootfs":
    raise SystemExit(body)
if body.get("builder_phase") != "complete":
    raise SystemExit(body)
if body.get("output_path") != out:
    raise SystemExit(body)
if body.get("size_bytes") != os.path.getsize(out):
    raise SystemExit(body)
if not body.get("digest"):
    raise SystemExit(body)
PY

echo "rootfs OCI smoke passed"
