#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-firecracker-console.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
WORKSPACE="console-smoke"
IMAGE="docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f"
EXPECTED_KERNEL_SHA="4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0"

cleanup() {
  status="$?"
  if [ "$status" -eq 0 ] && [ -x "$CLI" ]; then
    "$CLI" stop "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" delete "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_FIRECRACKER_CONSOLE_SMOKE:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept firecracker console smoke state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    e2e_skip "firecracker console parity smoke requires Linux amd64"
    ;;
esac

if [ ! -e /dev/kvm ]; then
  e2e_skip "/dev/kvm is not visible; run this smoke outside sandboxed environments"
fi

if [ -n "${MICROAGENT_FIRECRACKER:-}" ]; then
  firecracker="$MICROAGENT_FIRECRACKER"
elif command -v firecracker >/dev/null 2>&1; then
  firecracker="$(command -v firecracker)"
elif command -v brew >/dev/null 2>&1; then
  formula_prefix="$(brew --prefix microagent 2>/dev/null || true)"
  firecracker="$formula_prefix/libexec/firecracker"
else
  firecracker=""
fi

if [ ! -x "${firecracker:-}" ]; then
  e2e_skip "firecracker binary not found; install microagent or set MICROAGENT_FIRECRACKER"
fi

export GOCACHE="$STATE_DIR/gocache"
export GOMODCACHE="$STATE_DIR/gomodcache"
export GOFLAGS="${GOFLAGS:-} -modcacherw"
export MICROAGENT_FIRECRACKER="$firecracker"
export MICROAGENT_FIRECRACKER_SUPERVISOR="$SUPERVISOR"

(
  cd "$ROOT"
  go build -buildvcs=false -o "$CLI" ./cmd/microagent
  go build -buildvcs=false -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
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

"$CLI" create "$WORKSPACE" \
  --image "$IMAGE" \
  --arch amd64 \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR" \
  --size-mib 128 \
  --result-port 0 >"$STATE_DIR/create.json"

if "$CLI" connect "$WORKSPACE" --state-dir "$STATE_DIR" --send "echo SHOULD_NOT_RUN" >"$STATE_DIR/prepared-connect.txt" 2>"$STATE_DIR/prepared-connect.err"; then
  echo "connect unexpectedly succeeded before the workspace was started" >&2
  exit 1
fi
grep -q "console input is unavailable in state prepared" "$STATE_DIR/prepared-connect.err"

"$CLI" start "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --kernel "$kernel_path" >"$STATE_DIR/start.json"

for _ in $(seq 1 50); do
  if [ -p "$STATE_DIR/$WORKSPACE/serial.in" ]; then
    break
  fi
  sleep 0.1
done
test -p "$STATE_DIR/$WORKSPACE/serial.in"

"$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "echo CONNECT_READY" \
  --timeout 10 >"$STATE_DIR/send.txt"

printf '\035' | "$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --ready-timeout 10 >"$STATE_DIR/detach.txt"

"$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-after-detach.json"
"$CLI" logs "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/logs.txt"

python3 - "$STATE_DIR/create.json" "$STATE_DIR/start.json" "$STATE_DIR/send.txt" "$STATE_DIR/status-after-detach.json" "$STATE_DIR/logs.txt" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    create = json.load(f)
with open(sys.argv[2], "r", encoding="utf-8") as f:
    start = json.load(f)
with open(sys.argv[3], "r", encoding="utf-8", errors="replace") as f:
    send = f.read()
with open(sys.argv[4], "r", encoding="utf-8") as f:
    status = json.load(f)
with open(sys.argv[5], "r", encoding="utf-8", errors="replace") as f:
    logs = f.read()

if create["response"]["backend"] != "firecracker":
    raise SystemExit(create)
if start["response"]["event"]["state"] != "running":
    raise SystemExit(start)
if "CONNECT_READY" not in send:
    raise SystemExit("connect output did not reach the guest shell")
if status["event"]["state"] != "running":
    raise SystemExit("Ctrl-] detach stopped the workspace")
if "microagent-init: starting" not in logs:
    raise SystemExit("serial output was not available through logs")
PY

"$CLI" stop "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/stop.json"
"$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete.json"

echo "firecracker console parity smoke passed"
