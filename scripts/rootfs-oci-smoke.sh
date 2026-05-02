#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-rootfs-smoke.XXXXXX")"
OUT="$STATE_DIR/busybox-rootfs.ext4"
IMAGE="${MICROAGENT_ROOTFS_SMOKE_IMAGE:-docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6}"
ARCH="${MICROAGENT_ROOTFS_SMOKE_ARCH:-arm64}"

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
