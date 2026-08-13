#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SUPERVISOR="$ROOT/supervisors/applevf/.build/debug/microagent-applevf-supervisor"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-cli-smoke.XXXXXX")"
KERNEL="$STATE_DIR/kernel"
ROOTFS="$STATE_DIR/rootfs.ext4"
GUEST_INIT="$STATE_DIR/microagent-guestinit-arm64"

cleanup() {
  rm -rf "$STATE_DIR"
}
trap cleanup EXIT

touch "$KERNEL" "$ROOTFS"

go build -o "$STATE_DIR/microagent" "$ROOT/cmd/microagent"
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -o "$GUEST_INIT" "$ROOT/cmd/microagent-guestinit"
swift build --package-path "$ROOT/supervisors/applevf" --disable-sandbox >/dev/null

run_cli() {
  "$STATE_DIR/microagent" "$@" --supervisor "$SUPERVISOR"
}

assert_json() {
  local response="$1"
  local want_ok="$2"
  local want_state="${3:-}"
  python3 - "$want_ok" "$want_state" "$response" <<'PY'
import json
import sys

want_ok = sys.argv[1] == "true"
want_state = sys.argv[2]
body = json.loads(sys.argv[3])
if body.get("ok") is not want_ok:
    raise SystemExit(f"ok={body.get('ok')}, want {want_ok}: {body}")
if body.get("backend") != "apple-vf":
    raise SystemExit(f"backend={body.get('backend')!r}: {body}")
if want_state:
    got = ((body.get("event") or {}).get("state"))
    if got != want_state:
        raise SystemExit(f"state={got!r}, want {want_state!r}: {body}")
PY
}

doctor_response="$(run_cli doctor)"
assert_json "$doctor_response" true
python3 - "$GUEST_INIT" "$doctor_response" <<'PY'
import json
import os
import sys

guest_init, raw = sys.argv[1:]
body = json.loads(raw)
host = body.get("host") or {}
if body.get("verdict") == "failed":
    raise SystemExit(f"verdict={body.get('verdict')!r}: {body}")
if host.get("guestInitAvailable") is not True:
    raise SystemExit(f"guestInitAvailable={host.get('guestInitAvailable')!r}: {body}")
got = os.path.realpath(host.get("guestInitPath") or "")
want = os.path.realpath(guest_init)
if got != want:
    raise SystemExit(f"guestInitPath={got!r}, want {want!r}: {body}")
PY

dry_run_response="$(run_cli create --dry-run --id agent-smoke --kernel "$KERNEL" --rootfs "$ROOTFS" --state-dir "$STATE_DIR" --vsock 1024=127.0.0.1:8200)"
assert_json "$dry_run_response" true prepared
test ! -e "$STATE_DIR/agent-smoke"

create_response="$(run_cli create --id agent-smoke --kernel "$KERNEL" --rootfs "$ROOTFS" --state-dir "$STATE_DIR" --vsock 1024=127.0.0.1:8200)"
assert_json "$create_response" true prepared
test -f "$STATE_DIR/agent-smoke/event.json"

status_response="$(run_cli status agent-smoke --state-dir "$STATE_DIR")"
assert_json "$status_response" true prepared

# stop is an alias of halt: it records the halted state, identical to halt.
stop_response="$(run_cli stop agent-smoke --state-dir "$STATE_DIR")"
assert_json "$stop_response" true halted

kill_response="$(run_cli kill agent-smoke --state-dir "$STATE_DIR" --reason "lifecycle smoke cleanup" --yes)"
assert_json "$kill_response" true stopped
python3 - "$kill_response" <<'PY'
import json
import sys

body = json.loads(sys.argv[1])
if (body.get("event") or {}).get("detail") != "forced":
    raise SystemExit(body)
PY

if start_response="$(run_cli start --id agent-smoke --kernel "$KERNEL" --rootfs "$ROOTFS" --state-dir "$STATE_DIR" 2>/dev/null)"; then
  echo "expected start to return a nonzero status" >&2
  exit 1
fi
assert_json "$start_response" false

delete_response="$(run_cli delete agent-smoke --state-dir "$STATE_DIR" --yes)"
assert_json "$delete_response" true stopped
test ! -e "$STATE_DIR/agent-smoke"

request_file="$STATE_DIR/request.json"
python3 - "$STATE_DIR" "$KERNEL" "$ROOTFS" <<'PY' >"$request_file"
import json
import sys

state_dir, kernel, rootfs = sys.argv[1:]
print(json.dumps({
    "identity": {
        "requestID": "req-json",
        "runtimeID": "agent-json",
        "role": "workload",
        "backend": "apple-vf",
    },
    "config": {
        "kernelPath": kernel,
        "rootfsPath": rootfs,
        "stateDir": state_dir,
    },
}))
PY

json_file_response="$(run_cli create --request-json "$request_file")"
assert_json "$json_file_response" true prepared

json_stdin_response="$(run_cli create --request-json - <"$request_file")"
assert_json "$json_stdin_response" true prepared

echo "cli lifecycle smoke passed"
