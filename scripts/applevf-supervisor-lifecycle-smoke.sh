#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SUPERVISOR="$ROOT/supervisors/applevf/.build/debug/microagent-applevf-supervisor"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-supervisor-smoke.XXXXXX")"
KERNEL="$STATE_DIR/kernel"
ROOTFS="$STATE_DIR/rootfs.ext4"

cleanup() {
  rm -rf "$STATE_DIR"
}
trap cleanup EXIT

touch "$KERNEL" "$ROOTFS"

swift build --package-path "$ROOT/supervisors/applevf" --disable-sandbox >/dev/null

request() {
  local command="$1"
  python3 - "$command" "$STATE_DIR" "$KERNEL" "$ROOTFS" <<'PY'
import json
import sys

command, state_dir, kernel, rootfs = sys.argv[1:]
body = {
    "command": command,
    "identity": {
        "requestID": "req-smoke",
        "runtimeID": "agent-smoke",
        "role": "workload",
        "backend": "apple-vf",
    },
    "config": {
        "kernelPath": kernel,
        "rootfsPath": rootfs,
        "stateDir": state_dir,
        "memoryMiB": 512,
        "cpuCount": 2,
        "vsockListeners": [{"port": 1024, "target": "127.0.0.1:8200"}],
    },
}
print(json.dumps(body))
PY
}

request_state_only() {
  local command="$1"
  python3 - "$command" "$STATE_DIR" <<'PY'
import json
import sys

command, state_dir = sys.argv[1:]
body = {
    "command": command,
    "identity": {
        "requestID": "req-smoke",
        "runtimeID": "agent-smoke",
        "role": "workload",
        "backend": "apple-vf",
    },
    "config": {"kernelPath": "", "rootfsPath": "", "stateDir": state_dir},
}
print(json.dumps(body))
PY
}

assert_response() {
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

host_response="$("$SUPERVISOR" <<< '{"command":"host"}')"
python3 - "$host_response" <<'PY'
import json
import sys

body = json.loads(sys.argv[1])
if body.get("ok") is not True:
    raise SystemExit(body)
host = body.get("host") or {}
if host.get("backend") != "apple-vf":
    raise SystemExit(body)
PY

check_response="$(request check | "$SUPERVISOR")"
assert_response "$check_response" true prepared

prepare_response="$(request prepare | "$SUPERVISOR")"
assert_response "$prepare_response" true prepared
test -f "$STATE_DIR/agent-smoke/event.json"
test -f "$STATE_DIR/agent-smoke/config.json"

inspect_response="$(request_state_only inspect | "$SUPERVISOR")"
assert_response "$inspect_response" true prepared

stop_response="$(request_state_only stop | "$SUPERVISOR")"
assert_response "$stop_response" true stopped

kill_response="$(request_state_only kill | "$SUPERVISOR")"
assert_response "$kill_response" true stopped
python3 - "$kill_response" <<'PY'
import json
import sys

body = json.loads(sys.argv[1])
if (body.get("event") or {}).get("detail") != "forced":
    raise SystemExit(body)
PY

if start_response="$(request start | "$SUPERVISOR" 2>/dev/null)"; then
  echo "expected start to return a nonzero status" >&2
  exit 1
fi
assert_response "$start_response" false
python3 - "$start_response" <<'PY'
import json
import sys

body = json.loads(sys.argv[1])
if not body.get("error"):
    raise SystemExit(body)
PY

delete_response="$(request_state_only delete | "$SUPERVISOR")"
assert_response "$delete_response" true stopped
test ! -e "$STATE_DIR/agent-smoke"

invalid_response="$STATE_DIR/invalid.json"
if "$SUPERVISOR" <<< '{"command":"check"}' >"$invalid_response" 2>/dev/null; then
  echo "expected invalid supervisor request to fail" >&2
  exit 1
fi
python3 - "$invalid_response" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    body = json.load(handle)
if body.get("ok") is not False or "identity is required" not in body.get("error", ""):
    raise SystemExit(body)
PY

echo "supervisor lifecycle smoke passed"
