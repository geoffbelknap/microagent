#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-firecracker-workspace.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
CONNECT_RESULT="$STATE_DIR/connect.txt"
IMAGE="docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f"
EXPECTED_KERNEL_SHA="4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0"

cleanup() {
  status="$?"
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_FIRECRACKER_WORKSPACE_SMOKE:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept firecracker workspace smoke state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    echo "firecracker workspace smoke requires Linux amd64" >&2
    exit 2
    ;;
esac

if [ ! -e /dev/kvm ]; then
  echo "/dev/kvm is not visible; run this smoke outside sandboxed environments" >&2
  exit 2
fi

if [ -n "${MICROAGENT_FIRECRACKER:-}" ]; then
  firecracker="$MICROAGENT_FIRECRACKER"
elif command -v firecracker >/dev/null 2>&1; then
  firecracker="$(command -v firecracker)"
elif command -v brew >/dev/null 2>&1; then
  formula_prefix="$(brew --prefix microagent-kit 2>/dev/null || true)"
  firecracker="$formula_prefix/libexec/firecracker"
else
  firecracker=""
fi

if [ ! -x "${firecracker:-}" ]; then
  echo "firecracker binary not found; install microagent-kit or set MICROAGENT_FIRECRACKER" >&2
  exit 2
fi

export GOCACHE="$STATE_DIR/gocache"
export GOMODCACHE="$STATE_DIR/gomodcache"
export GOFLAGS="${GOFLAGS:-} -modcacherw"
export MICROAGENT_FIRECRACKER="$firecracker"
export MICROAGENT_FIRECRACKER_SUPERVISOR="$SUPERVISOR"

(
  cd "$ROOT"
  go build -o "$CLI" ./cmd/microagent
  go build -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

"$CLI" kernel install --backend firecracker --arch amd64 >"$STATE_DIR/kernel-install.json"

kernel_path="$(python3 - "$STATE_DIR/kernel-install.json" "$EXPECTED_KERNEL_SHA" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)
if result.get("sha256") != sys.argv[2]:
    raise SystemExit(result)
print(result["path"])
PY
)"

"$CLI" doctor >"$STATE_DIR/doctor.json"
python3 - "$STATE_DIR/doctor.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)
assert result["ok"] is True
assert result["backend"] == "firecracker"
assert result["host"]["kvmAvailable"] is True
assert result["host"]["vsockAvailable"] is True
PY

"$CLI" create connect-smoke \
  --image "$IMAGE" \
  --arch amd64 \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR/connect" \
  --size-mib 128 \
  --result-port 0 >"$STATE_DIR/connect-create.json"

"$CLI" status connect-smoke --state-dir "$STATE_DIR/connect" >"$STATE_DIR/connect-status-prepared.json"
if "$CLI" connect connect-smoke --state-dir "$STATE_DIR/connect" --send "echo CONNECT_READY" >"$CONNECT_RESULT" 2>"$STATE_DIR/connect.err"; then
  echo "firecracker connect unexpectedly succeeded" >&2
  exit 1
fi
grep -q "console input is not ready" "$STATE_DIR/connect.err"
"$CLI" logs connect-smoke --state-dir "$STATE_DIR/connect" >"$STATE_DIR/connect-logs.txt"
"$CLI" ps --state-dir "$STATE_DIR/connect" >"$STATE_DIR/connect-ps.json"

python3 - "$STATE_DIR/connect-create.json" "$CONNECT_RESULT" "$STATE_DIR/connect-logs.txt" "$STATE_DIR/connect-ps.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    create = json.load(f)
with open(sys.argv[2], "r", encoding="utf-8", errors="replace") as f:
    connect = f.read()
with open(sys.argv[3], "r", encoding="utf-8", errors="replace") as f:
    logs = f.read()
with open(sys.argv[4], "r", encoding="utf-8") as f:
    ps = json.load(f)

if create["response"]["backend"] != "firecracker":
    raise SystemExit(create)
if create["response"]["event"]["state"] != "prepared":
    raise SystemExit(create)
if connect.strip():
    raise SystemExit(connect)
if "Running Firecracker" in logs:
    raise SystemExit("logs should be empty for a prepared-only workspace")
if not any(entry["name"] == "connect-smoke" for entry in ps["workspaces"]):
    raise SystemExit(ps)
PY

"$CLI" delete connect-smoke --state-dir "$STATE_DIR/connect" >"$STATE_DIR/connect-delete.json"
test ! -e "$STATE_DIR/connect/connect-smoke"
test ! -e "$STATE_DIR/connect/workspaces/connect-smoke"

"$CLI" create lifecycle-smoke \
  --image "$IMAGE" \
  --arch amd64 \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR/lifecycle" \
  --size-mib 128 \
  --result-port 0 \
  --entrypoint "sleep 60" >"$STATE_DIR/lifecycle-create.json"

"$CLI" start lifecycle-smoke --state-dir "$STATE_DIR/lifecycle" --kernel "$kernel_path" >"$STATE_DIR/lifecycle-start.json"
if "$CLI" delete lifecycle-smoke --state-dir "$STATE_DIR/lifecycle" >"$STATE_DIR/lifecycle-delete-running.json" 2>"$STATE_DIR/lifecycle-delete-running.err"; then
  echo "delete succeeded while Firecracker workspace was running" >&2
  exit 1
fi
"$CLI" stop lifecycle-smoke --state-dir "$STATE_DIR/lifecycle" >"$STATE_DIR/lifecycle-stop.json"
"$CLI" delete lifecycle-smoke --state-dir "$STATE_DIR/lifecycle" >"$STATE_DIR/lifecycle-delete.json"

python3 - "$STATE_DIR/lifecycle-start.json" "$STATE_DIR/lifecycle-stop.json" "$STATE_DIR/lifecycle-delete.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    start = json.load(f)
with open(sys.argv[2], "r", encoding="utf-8") as f:
    stop = json.load(f)
with open(sys.argv[3], "r", encoding="utf-8") as f:
    delete = json.load(f)

if start["response"]["event"]["state"] != "running":
    raise SystemExit(start)
if stop["event"]["state"] != "stopped":
    raise SystemExit(stop)
if delete["event"]["state"] != "stopped":
    raise SystemExit(delete)
PY

echo "firecracker workspace smoke passed"
