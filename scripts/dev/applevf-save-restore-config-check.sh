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
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-applevf-save-restore-check.XXXXXX")"
WORKSPACE="save-restore-check"
GUEST_INIT="$STATE_DIR/microagent-guestinit"
CREATE_RESULT="$STATE_DIR/create.json"
REQUEST_JSON="$STATE_DIR/save-restore-request.json"
CHECK_RESULT="$STATE_DIR/save-restore-check.json"

cleanup() {
  status="$?"
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_APPLEVF_SAVE_RESTORE_CHECK:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept Apple VF save/restore config-check state at $STATE_DIR"
  fi
}
trap cleanup EXIT

if [ "$(uname -s)" != "Darwin" ] || [ "$(uname -m)" != "arm64" ]; then
  e2e_skip "Apple VF save/restore config check requires macOS on Apple silicon"
fi
if [ ! -r "$KERNEL" ]; then
  e2e_skip "kernel is not readable at $KERNEL"
fi
if [ ! -x "$SUPERVISOR" ]; then
  e2e_skip "supervisor is not executable at $SUPERVISOR; run scripts/dev/applevf-supervisor-build.sh"
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

"$STATE_DIR/microagent" create \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --size-mib "${MICROAGENT_APPLEVF_BOOT_SIZE_MIB:-128}" \
  --mke2fs "$MKE2FS" \
  --exec "true" \
  --name "$WORKSPACE" \
  --kernel "$KERNEL" \
  --state-dir "$STATE_DIR" \
  --memory "${MICROAGENT_APPLEVF_BOOT_MEMORY_MIB:-512}" \
  --cpus "${MICROAGENT_APPLEVF_BOOT_CPUS:-2}" \
  --timeout "${MICROAGENT_APPLEVF_BOOT_TIMEOUT_SECONDS:-30}" \
  --guest-init "$GUEST_INIT" \
  --supervisor "$SUPERVISOR" >"$CREATE_RESULT"

python3 - "$STATE_DIR" "$WORKSPACE" "$REQUEST_JSON" <<'PY'
import json
import sys
from pathlib import Path

state_dir = Path(sys.argv[1])
workspace = sys.argv[2]
request_path = Path(sys.argv[3])
config_path = state_dir / workspace / "config.json"
config = json.loads(config_path.read_text(encoding="utf-8"))
request = {
    "command": "check",
    "identity": {
        "requestID": "save-restore-config-check",
        "runtimeID": workspace,
        "role": "workload",
        "backend": "apple-vf",
    },
    "config": config,
}
request_path.write_text(json.dumps(request), encoding="utf-8")
PY

MICROAGENT_EGRESS_DATAPATH_BIN="$STATE_DIR/microagent" \
  "$SUPERVISOR" --save-restore-config-check --request "$REQUEST_JSON" >"$CHECK_RESULT"

python3 - "$CHECK_RESULT" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)
if not result.get("ok"):
    raise SystemExit(result.get("error", "Apple VF save/restore config check failed"))
event = result.get("event") or {}
if event.get("state") != "prepared":
    raise SystemExit(f"unexpected event state: {event.get('state')}")
print("Apple VF save/restore config check passed")
PY
