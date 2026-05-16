#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-supervision.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
IMAGE="${MICROAGENT_NATS_IMAGE:-docker.io/library/nats@sha256:6e0cca2c6da79f0a3542ec5a3319dd10b1b05f5d8e8949afa8e9cdf6314bbf6c}"
EXPECTED_KERNEL_SHA="4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0"
SUPERVISE_PID=""
CANCEL_SUPERVISE_PID=""

cleanup() {
  status="$?"
  if [ -n "$SUPERVISE_PID" ]; then
    kill "$SUPERVISE_PID" >/dev/null 2>&1 || true
  fi
  if [ -n "$CANCEL_SUPERVISE_PID" ]; then
    kill "$CANCEL_SUPERVISE_PID" >/dev/null 2>&1 || true
  fi
  if [ -x "$CLI" ]; then
    for workspace in supervise-never supervise-on-failure supervise-always supervise-cancel; do
      "$CLI" stop "$workspace" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      "$CLI" delete "$workspace" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    done
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_SUPERVISION:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E supervision state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    echo "microagent E2E supervision requires Linux amd64" >&2
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
  formula_prefix="$(brew --prefix microagent 2>/dev/null || true)"
  firecracker="$formula_prefix/libexec/firecracker"
else
  firecracker=""
fi

if [ ! -x "${firecracker:-}" ]; then
  echo "Linux microagent E2E requires the Firecracker backend binary; install firecracker on PATH or set MICROAGENT_FIRECRACKER" >&2
  exit 2
fi

export GOCACHE="$STATE_DIR/gocache"
export GOMODCACHE="$STATE_DIR/gomodcache"
export GOFLAGS="${GOFLAGS:-} -modcacherw"
export MICROAGENT_FIRECRACKER="$firecracker"
export MICROAGENT_FIRECRACKER_SUPERVISOR="$SUPERVISOR"

wait_for_state() {
  workspace="$1"
  wanted="$2"
  output="$3"
  deadline="$((SECONDS + 60))"
  while true; do
    "$CLI" status "$workspace" --state-dir "$STATE_DIR" >"$output" 2>"$output.err" || true
    if python3 - "$output" "$wanted" <<'PY'
import json
import sys

try:
    with open(sys.argv[1], "r", encoding="utf-8") as f:
        status = json.load(f)
except Exception:
    raise SystemExit(1)
if status.get("event", {}).get("state") == sys.argv[2]:
    raise SystemExit(0)
raise SystemExit(1)
PY
    then
      return 0
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "workspace $workspace did not reach $wanted" >&2
      cat "$output" >&2 || true
      cat "$output.err" >&2 || true
      return 1
    fi
    sleep 1
  done
}

process_is_active() {
  pid="$1"
  [ -n "$pid" ] && ps -p "$pid" >/dev/null 2>&1
}

wait_for_process_exit() {
  pid="$1"
  deadline="$((SECONDS + 20))"
  while process_is_active "$pid"; do
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "process $pid did not exit" >&2
      ps -fp "$pid" >&2 || true
      return 1
    fi
    sleep 1
  done
}

(
  cd "$ROOT"
  go build -buildvcs=false -o "$CLI" ./cmd/microagent
  go build -buildvcs=false -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

default_kernel="${MICROAGENT_FIRECRACKER_KERNEL:-$HOME/.microagent/kernels/firecracker/amd64/Image}"
if [ -f "$default_kernel" ] && [ "$(sha256sum "$default_kernel" | awk '{print $1}')" = "$EXPECTED_KERNEL_SHA" ]; then
  kernel_path="$default_kernel"
  printf '{"path":%s,"sha256":%s}\n' \
    "$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$kernel_path")" \
    "$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$EXPECTED_KERNEL_SHA")" >"$STATE_DIR/kernel-install.json"
else
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
fi

"$CLI" doctor >"$STATE_DIR/doctor.json"

"$CLI" create supervise-never \
  --image "$IMAGE" \
  --arch amd64 \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR" \
  --size-mib 192 \
  --network isolated \
  --restart never \
  --entrypoint "sleep 300" >"$STATE_DIR/create-never.json"
"$CLI" supervise supervise-never \
  --state-dir "$STATE_DIR" \
  --kernel "$kernel_path" \
  --interval 1 \
  --max-restarts 1 >"$STATE_DIR/supervise-never.json"
"$CLI" status supervise-never --state-dir "$STATE_DIR" >"$STATE_DIR/status-never.json"

"$CLI" create supervise-on-failure \
  --image "$IMAGE" \
  --arch amd64 \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR" \
  --size-mib 192 \
  --network isolated \
  --restart on-failure \
  --entrypoint "sleep 300" >"$STATE_DIR/create-on-failure.json"
"$CLI" supervise supervise-on-failure \
  --state-dir "$STATE_DIR" \
  --kernel "$STATE_DIR/missing-kernel" \
  --interval 1 \
  --max-restarts 2 >"$STATE_DIR/supervise-on-failure.json"
"$CLI" status supervise-on-failure --state-dir "$STATE_DIR" >"$STATE_DIR/status-on-failure-final.json" || true

"$CLI" create supervise-always \
  --image "$IMAGE" \
  --arch amd64 \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR" \
  --size-mib 192 \
  --network isolated \
  --restart always \
  --entrypoint "sleep 300" >"$STATE_DIR/create-always.json"
"$CLI" supervise supervise-always \
  --state-dir "$STATE_DIR" \
  --kernel "$kernel_path" \
  --interval 1 \
  --max-restarts 2 >"$STATE_DIR/supervise-always.json" &
SUPERVISE_PID="$!"
wait_for_state supervise-always running "$STATE_DIR/status-always-running-1.json"
"$CLI" stop supervise-always --state-dir "$STATE_DIR" >"$STATE_DIR/stop-always-1.json"
wait_for_state supervise-always running "$STATE_DIR/status-always-running-2.json"
"$CLI" stop supervise-always --state-dir "$STATE_DIR" >"$STATE_DIR/stop-always-2.json"
wait "$SUPERVISE_PID"
SUPERVISE_PID=""
"$CLI" status supervise-always --state-dir "$STATE_DIR" >"$STATE_DIR/status-always-final.json"
"$CLI" logs supervise-always --state-dir "$STATE_DIR" >"$STATE_DIR/logs-always.txt"

"$CLI" create supervise-cancel \
  --image "$IMAGE" \
  --arch amd64 \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR" \
  --size-mib 192 \
  --network isolated \
  --restart always \
  --entrypoint "sleep 300" >"$STATE_DIR/create-cancel.json"
"$CLI" supervise supervise-cancel \
  --state-dir "$STATE_DIR" \
  --kernel "$kernel_path" \
  --interval 1 \
  --max-restarts 0 >"$STATE_DIR/supervise-cancel.json" &
CANCEL_SUPERVISE_PID="$!"
wait_for_state supervise-cancel running "$STATE_DIR/status-cancel-running.json"
if ! process_is_active "$CANCEL_SUPERVISE_PID"; then
  echo "supervise-cancel process exited before cancellation" >&2
  cat "$STATE_DIR/supervise-cancel.json" >&2 || true
  exit 1
fi
python3 - "$STATE_DIR/supervise-cancel/runtime.json" "$STATE_DIR/cancel-runtime-pid.txt" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    runtime = json.load(f)
pid = runtime.get("pid") or 0
if pid <= 0:
    raise SystemExit(runtime)
with open(sys.argv[2], "w", encoding="utf-8") as f:
    f.write(str(pid))
PY
cancel_runtime_pid="$(cat "$STATE_DIR/cancel-runtime-pid.txt")"
kill "$CANCEL_SUPERVISE_PID"
wait_for_process_exit "$CANCEL_SUPERVISE_PID"
CANCEL_SUPERVISE_PID=""
"$CLI" status supervise-cancel --state-dir "$STATE_DIR" >"$STATE_DIR/status-cancel-after-supervise-kill.json" || true
if ! process_is_active "$cancel_runtime_pid"; then
  echo "workspace runtime pid $cancel_runtime_pid exited when only the supervise process was killed" >&2
  exit 1
fi
"$CLI" stop supervise-cancel --state-dir "$STATE_DIR" >"$STATE_DIR/stop-cancel.json"
wait_for_state supervise-cancel stopped "$STATE_DIR/status-cancel-stopped.json"
wait_for_process_exit "$cancel_runtime_pid"
sleep 3
"$CLI" status supervise-cancel --state-dir "$STATE_DIR" >"$STATE_DIR/status-cancel-no-restart.json" || true

python3 - "$STATE_DIR" <<'PY'
import json
import os
import sys

state_dir = sys.argv[1]

def read_json(name):
    with open(os.path.join(state_dir, name), "r", encoding="utf-8") as f:
        return json.load(f)

doctor = read_json("doctor.json")
never = read_json("supervise-never.json")
never_status = read_json("status-never.json")
on_failure = read_json("supervise-on-failure.json")
always = read_json("supervise-always.json")
always_status = read_json("status-always-final.json")
cancel_after_kill = read_json("status-cancel-after-supervise-kill.json")
cancel_stopped = read_json("status-cancel-stopped.json")
cancel_no_restart = read_json("status-cancel-no-restart.json")

if doctor.get("ok") is not True:
    raise SystemExit(doctor)
if never.get("policy") != "never" or never.get("restarts") != 0 or never.get("stopped") is not True:
    raise SystemExit(never)
if never_status.get("event", {}).get("state") != "prepared":
    raise SystemExit(never_status)
if on_failure.get("policy") != "on-failure" or on_failure.get("restarts") != 2 or on_failure.get("stopped") is not True:
    raise SystemExit(on_failure)
if on_failure.get("final_state") != "failed":
    raise SystemExit(on_failure)
if always.get("policy") != "always" or always.get("restarts") != 2 or always.get("stopped") is not True:
    raise SystemExit(always)
if always.get("final_state") != "stopped":
    raise SystemExit(always)
if always_status.get("event", {}).get("state") != "stopped":
    raise SystemExit(always_status)
if cancel_after_kill.get("event", {}).get("state") != "running":
    raise SystemExit(cancel_after_kill)
if cancel_stopped.get("event", {}).get("state") != "stopped":
    raise SystemExit(cancel_stopped)
if cancel_no_restart.get("event", {}).get("state") != "stopped":
    raise SystemExit(cancel_no_restart)
PY

for workspace in supervise-never supervise-on-failure supervise-always supervise-cancel; do
  "$CLI" delete "$workspace" --state-dir "$STATE_DIR" >"$STATE_DIR/delete-${workspace}.json" || true
done

echo "microagent E2E supervision passed"
