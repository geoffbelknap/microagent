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
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-applevf-save-state-seatbelt.XXXXXX")"
WORKSPACE="save-state-seatbelt"
GUEST_INIT="$STATE_DIR/microagent-guestinit"
CLI="$STATE_DIR/microagent"

cleanup() {
  status="$?"
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_APPLEVF_SAVE_STATE_CHECK:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept Apple VF save-state Seatbelt-check state at $STATE_DIR"
  fi
}
trap cleanup EXIT

if [ "$(uname -s)" != "Darwin" ] || [ "$(uname -m)" != "arm64" ]; then
  e2e_skip "Apple VF save-state Seatbelt check requires macOS on Apple silicon"
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
  go build -o "$CLI" ./cmd/microagent
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

prepare_workspace() {
  mode="$1"
  mode_dir="$STATE_DIR/$mode"
  mkdir -p "$mode_dir"
  "$CLI" create \
    --image "$IMAGE" \
    --arch "$ARCH" \
    --size-mib "${MICROAGENT_APPLEVF_BOOT_SIZE_MIB:-128}" \
    --mke2fs "$MKE2FS" \
    --exec "true" \
    --name "$WORKSPACE" \
    --kernel "$KERNEL" \
    --state-dir "$mode_dir" \
    --memory "${MICROAGENT_APPLEVF_BOOT_MEMORY_MIB:-512}" \
    --cpus "${MICROAGENT_APPLEVF_BOOT_CPUS:-2}" \
    --timeout "${MICROAGENT_APPLEVF_BOOT_TIMEOUT_SECONDS:-30}" \
    --guest-init "$GUEST_INIT" \
    --supervisor "$SUPERVISOR" >"$mode_dir/create.json"

  python3 - "$mode_dir" "$WORKSPACE" "$mode_dir/request.json" <<'PY'
import json
import sys
from pathlib import Path

state_dir = Path(sys.argv[1])
workspace = sys.argv[2]
request_path = Path(sys.argv[3])
config = json.loads((state_dir / workspace / "config.json").read_text(encoding="utf-8"))
request = {
    "command": "check",
    "identity": {
        "requestID": f"save-state-seatbelt-{state_dir.name}",
        "runtimeID": workspace,
        "role": "workload",
        "backend": "apple-vf",
    },
    "config": config,
}
request_path.write_text(json.dumps(request), encoding="utf-8")
PY
}

run_check() {
  mode="$1"
  mode_dir="$STATE_DIR/$mode"
  save_state_path="$mode_dir/$WORKSPACE/save-state-check.vmstate"
  MICROAGENT_EGRESS_DATAPATH_BIN="$CLI" \
    "$SUPERVISOR" --save-state-check \
      --request "$mode_dir/request.json" \
      --mode "$mode" \
      --save-state-path "$save_state_path" >"$mode_dir/save-state-check.json"
}

summarize_result() {
  mode="$1"
  result="$STATE_DIR/$mode/save-state-check.json"
  python3 - "$mode" "$result" <<'PY'
import json
import sys

mode = sys.argv[1]
with open(sys.argv[2], "r", encoding="utf-8") as f:
    result = json.load(f)
if result.get("ok"):
    event = result.get("event") or {}
    print(f"{mode}: ok ({event.get('detail', 'saved')})")
else:
    print(f"{mode}: failed: {result.get('error')}")
    raise SystemExit(1)
PY
}

prepare_workspace unconfined
prepare_workspace confined

set +e
run_check unconfined
unconfined_status="$?"
summarize_result unconfined
unconfined_summary_status="$?"
set -e

if [ "$unconfined_status" -ne 0 ] || [ "$unconfined_summary_status" -ne 0 ]; then
  echo "unconfined saveMachineStateTo failed; this is not only a Seatbelt profile denial"
  exit 1
fi

if command -v log >/dev/null 2>&1; then
  log stream --style compact --predicate 'sender == "Sandbox" OR eventMessage CONTAINS[c] "sandbox" OR eventMessage CONTAINS[c] "deny"' >"$STATE_DIR/confined/sandbox-live.log" 2>&1 &
  log_pid="$!"
  sleep 1
else
  log_pid=""
fi

set +e
run_check confined
confined_status="$?"
if [ -n "${log_pid:-}" ]; then
  kill "$log_pid" >/dev/null 2>&1 || true
  wait "$log_pid" >/dev/null 2>&1 || true
fi
summarize_result confined
confined_summary_status="$?"
set -e

if [ "$confined_status" -ne 0 ] || [ "$confined_summary_status" -ne 0 ]; then
  if command -v log >/dev/null 2>&1; then
    log show --last 5m --style compact --predicate 'sender == "Sandbox" OR eventMessage CONTAINS[c] "sandbox" OR eventMessage CONTAINS[c] "deny"' >"$STATE_DIR/confined/sandbox-recent.log" 2>&1 || true
  fi
  echo "confined saveMachineStateTo failed after unconfined succeeded; inspect $STATE_DIR/confined/*.log for Seatbelt denial details"
  exit 1
fi

echo "Apple VF saveMachineStateTo succeeded in both unconfined and confined modes"
