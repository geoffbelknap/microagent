#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-supervision.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
IMAGE="${MICROAGENT_NATS_IMAGE:-docker.io/library/nats@sha256:6e0cca2c6da79f0a3542ec5a3319dd10b1b05f5d8e8949afa8e9cdf6314bbf6c}"
SUPERVISE_PID=""
CANCEL_SUPERVISE_PID=""
SIGNAL_SUPERVISE_PID=""
HELPER_SUPERVISE_PID=""
STARTED_SUPERVISE_PID=""

cleanup() {
  status="$?"
  if [ -n "$SUPERVISE_PID" ]; then
    kill "$SUPERVISE_PID" >/dev/null 2>&1 || true
  fi
  if [ -n "$CANCEL_SUPERVISE_PID" ]; then
    kill "$CANCEL_SUPERVISE_PID" >/dev/null 2>&1 || true
  fi
  if [ -n "$SIGNAL_SUPERVISE_PID" ]; then
    kill "$SIGNAL_SUPERVISE_PID" >/dev/null 2>&1 || true
  fi
  if [ -n "$HELPER_SUPERVISE_PID" ]; then
    kill "$HELPER_SUPERVISE_PID" >/dev/null 2>&1 || true
  fi
  if [ -x "$CLI" ]; then
    for workspace in supervise-never supervise-on-failure supervise-always supervise-cancel supervise-sigint supervise-sigterm supervise-guest-fail supervise-vsock-helper; do
      "$CLI" stop "$workspace" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      "$CLI" delete "$workspace" --yes --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
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
    e2e_skip "microagent E2E supervision requires Linux amd64"
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
  e2e_skip "Linux microagent E2E requires the Firecracker backend binary; install firecracker on PATH or set MICROAGENT_FIRECRACKER"
fi

export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
export GOFLAGS="${GOFLAGS:-} -modcacherw"
export MICROAGENT_FIRECRACKER="$firecracker"
export MICROAGENT_FIRECRACKER_SUPERVISOR="$SUPERVISOR"

wait_for_state() {
  workspace="$1"
  wanted="$2"
  output="$3"
  deadline="$((SECONDS + ${MICROAGENT_E2E_WAIT_TIMEOUT:-60}))"
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
  deadline="$((SECONDS + ${MICROAGENT_E2E_WAIT_TIMEOUT:-20}))"
  while process_is_active "$pid"; do
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "process $pid did not exit" >&2
      ps -fp "$pid" >&2 || true
      return 1
    fi
    sleep 1
  done
}

start_supervise_background() {
  workspace="$1"
  output="$2"
  shift 2
  (
    trap - INT TERM
    exec "$CLI" supervise "$workspace" "$@"
  ) >"$output" &
  STARTED_SUPERVISE_PID="$!"
}

read_runtime_field() {
  workspace="$1"
  field="$2"
  output="$3"
  python3 - "$STATE_DIR/$workspace/runtime.json" "$field" "$output" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    runtime = json.load(f)
value = runtime.get(sys.argv[2]) or 0
if value <= 0:
    raise SystemExit(runtime)
with open(sys.argv[3], "w", encoding="utf-8") as f:
    f.write(str(value))
PY
}

signal_supervise_process() {
  workspace="$1"
  signal="$2"
  prefix="$3"
  "$CLI" create "$workspace" \
    --image "$IMAGE" \
    --arch amd64 \
    --kernel "$kernel_path" \
    --guest-init "$GUEST_INIT" \
    --state-dir "$STATE_DIR" \
    --size-mib 192 \
    --network isolated \
    --restart always \
    --entrypoint "sleep 300" >"$STATE_DIR/create-$prefix.json"
  start_supervise_background "$workspace" "$STATE_DIR/supervise-$prefix.json" \
    --state-dir "$STATE_DIR" \
    --kernel "$kernel_path" \
    --interval 1 \
    --max-restarts 0
  SIGNAL_SUPERVISE_PID="$STARTED_SUPERVISE_PID"
  wait_for_state "$workspace" running "$STATE_DIR/status-$prefix-running.json"
  read_runtime_field "$workspace" pid "$STATE_DIR/$prefix-runtime-pid.txt"
  runtime_pid="$(cat "$STATE_DIR/$prefix-runtime-pid.txt")"
  kill -s "$signal" "$SIGNAL_SUPERVISE_PID"
  wait_for_process_exit "$SIGNAL_SUPERVISE_PID"
  wait "$SIGNAL_SUPERVISE_PID" || true
  SIGNAL_SUPERVISE_PID=""
  "$CLI" status "$workspace" --state-dir "$STATE_DIR" >"$STATE_DIR/status-$prefix-after-signal.json" || true
  if ! process_is_active "$runtime_pid"; then
    echo "workspace runtime pid $runtime_pid exited after supervise received $signal" >&2
    exit 1
  fi
  # "$CLI" stop is a CLI-level alias of halt: a clean exit now records
  # `halted`, not `stopped`. No restart policy is exercised past this point
  # (both signal callers pass --max-restarts 0), so this is purely the
  # parked-workspace cleanup check, not a supervisor restart assertion.
  "$CLI" stop "$workspace" --state-dir "$STATE_DIR" >"$STATE_DIR/stop-$prefix.json"
  wait_for_state "$workspace" halted "$STATE_DIR/status-$prefix-stopped.json"
  wait_for_process_exit "$runtime_pid"
}

(
  cd "$ROOT"
  go build -buildvcs=false -o "$CLI" ./cmd/microagent
  go build -buildvcs=false -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

"$CLI" kernel install --backend linux-kvm --arch amd64 >"$STATE_DIR/kernel-install.json"
kernel_path="$(python3 - "$STATE_DIR/kernel-install.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)
print(result["path"])
PY
)"

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
start_supervise_background supervise-always "$STATE_DIR/supervise-always.json" \
  --state-dir "$STATE_DIR" \
  --kernel "$kernel_path" \
  --interval 1 \
  --max-restarts 2
SUPERVISE_PID="$STARTED_SUPERVISE_PID"
# Each `stop` performs an intentional guest shutdown: the workspace command
# (sleep 300) is killed and exits non-zero, but guest init marks the result
# powered_off so the supervisor observes a clean `halted` (the CLI `stop`
# verb is a registry-level alias of `halt`; a clean exit records `halted`,
# not `stopped`) and restarts under the `always` policy — ShouldRestart
# treats `halted` exactly like `stopped` for that policy, so the restart
# trigger is unaffected by the label. Two stops drive exactly two restarts,
# exhausting the --max-restarts 2 budget; the supervisor then exits with
# final_state=halted.
# The waits below are race-free by construction: wait_for_state polls with a
# deadline until each restart is back to `running`, and `wait "$SUPERVISE_PID"`
# blocks until the supervisor has written its terminal state and exited before
# the final status is read. A regression that misclassified an intentional
# shutdown as `failed` would surface as final_state != halted.
wait_for_state supervise-always running "$STATE_DIR/status-always-running-1.json"
"$CLI" stop supervise-always --state-dir "$STATE_DIR" >"$STATE_DIR/stop-always-1.json"
wait_for_state supervise-always running "$STATE_DIR/status-always-running-2.json"
"$CLI" stop supervise-always --state-dir "$STATE_DIR" >"$STATE_DIR/stop-always-2.json"
# `set -e` makes a non-zero supervisor exit abort the smoke; a clean
# restart-budget exhaustion returns 0.
wait "$SUPERVISE_PID"
SUPERVISE_PID=""
"$CLI" status supervise-always --state-dir "$STATE_DIR" >"$STATE_DIR/status-always-final.json"
"$CLI" logs supervise-always --state-dir "$STATE_DIR" >"$STATE_DIR/logs-always.txt"
if [ -n "${MICROAGENT_DEBUG_SUPERVISE:-}" ]; then
  echo "===== supervise-always sup-debug.log ====="
  cat "$STATE_DIR/supervise-always/sup-debug.log" 2>/dev/null || echo "(no debug log)"
  echo "===== supervise-always.json ====="
  cat "$STATE_DIR/supervise-always.json" 2>/dev/null
  echo "===== end supervise-always debug ====="
fi

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
start_supervise_background supervise-cancel "$STATE_DIR/supervise-cancel.json" \
  --state-dir "$STATE_DIR" \
  --kernel "$kernel_path" \
  --interval 1 \
  --max-restarts 0
CANCEL_SUPERVISE_PID="$STARTED_SUPERVISE_PID"
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
# The supervise loop was already killed above (no restart policy left to
# exercise); this is a direct park/cleanup check, so the CLI `stop` alias's
# clean-exit state is `halted`, not `stopped`.
"$CLI" stop supervise-cancel --state-dir "$STATE_DIR" >"$STATE_DIR/stop-cancel.json"
wait_for_state supervise-cancel halted "$STATE_DIR/status-cancel-stopped.json"
wait_for_process_exit "$cancel_runtime_pid"
sleep 3
"$CLI" status supervise-cancel --state-dir "$STATE_DIR" >"$STATE_DIR/status-cancel-no-restart.json" || true

signal_supervise_process supervise-sigint INT sigint
signal_supervise_process supervise-sigterm TERM sigterm

"$CLI" create supervise-guest-fail \
  --image "$IMAGE" \
  --arch amd64 \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR" \
  --size-mib 192 \
  --network isolated \
  --restart on-failure \
  --entrypoint "printf supervise-real-failure; exit 42" >"$STATE_DIR/create-guest-fail.json"
"$CLI" supervise supervise-guest-fail \
  --state-dir "$STATE_DIR" \
  --kernel "$kernel_path" \
  --interval 1 \
  --max-restarts 2 >"$STATE_DIR/supervise-guest-fail.json"
"$CLI" status supervise-guest-fail --state-dir "$STATE_DIR" >"$STATE_DIR/status-guest-fail-final.json" || true
"$CLI" result supervise-guest-fail --state-dir "$STATE_DIR" >"$STATE_DIR/result-guest-fail.json" || true

"$CLI" create supervise-vsock-helper \
  --image "$IMAGE" \
  --arch amd64 \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR" \
  --size-mib 192 \
  --network isolated \
  --mediation "2050=127.0.0.1:9" \
  --mediation-optional \
  --restart always \
  --entrypoint "sleep 300" >"$STATE_DIR/create-vsock-helper.json"
start_supervise_background supervise-vsock-helper "$STATE_DIR/supervise-vsock-helper.json" \
  --state-dir "$STATE_DIR" \
  --kernel "$kernel_path" \
  --interval 1 \
  --max-restarts 0
HELPER_SUPERVISE_PID="$STARTED_SUPERVISE_PID"
wait_for_state supervise-vsock-helper running "$STATE_DIR/status-vsock-helper-running.json"
read_runtime_field supervise-vsock-helper pid "$STATE_DIR/vsock-helper-runtime-pid.txt"
read_runtime_field supervise-vsock-helper vsockListenerPid "$STATE_DIR/vsock-helper-listener-pid.txt"
helper_runtime_pid="$(cat "$STATE_DIR/vsock-helper-runtime-pid.txt")"
vsock_listener_pid="$(cat "$STATE_DIR/vsock-helper-listener-pid.txt")"
if ! process_is_active "$vsock_listener_pid"; then
  echo "vsock listener pid $vsock_listener_pid was not active for supervised helper workspace" >&2
  exit 1
fi
kill -TERM "$HELPER_SUPERVISE_PID"
wait "$HELPER_SUPERVISE_PID" || true
HELPER_SUPERVISE_PID=""
# The supervise loop was already killed above (max-restarts 0); this is a
# direct park/cleanup check, so the CLI `stop` alias's clean-exit state is
# `halted`, not `stopped`.
"$CLI" stop supervise-vsock-helper --state-dir "$STATE_DIR" >"$STATE_DIR/stop-vsock-helper.json"
wait_for_state supervise-vsock-helper halted "$STATE_DIR/status-vsock-helper-stopped.json"
wait_for_process_exit "$helper_runtime_pid"
wait_for_process_exit "$vsock_listener_pid"
"$CLI" delete supervise-vsock-helper --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete-vsock-helper.json"
if process_is_active "$vsock_listener_pid"; then
  echo "vsock listener pid $vsock_listener_pid leaked after delete" >&2
  exit 1
fi

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
always_running_1 = read_json("status-always-running-1.json")
always_running_2 = read_json("status-always-running-2.json")
always_status = read_json("status-always-final.json")
cancel_after_kill = read_json("status-cancel-after-supervise-kill.json")
cancel_stopped = read_json("status-cancel-stopped.json")
cancel_no_restart = read_json("status-cancel-no-restart.json")
sigint_after_signal = read_json("status-sigint-after-signal.json")
sigint_stopped = read_json("status-sigint-stopped.json")
sigterm_after_signal = read_json("status-sigterm-after-signal.json")
sigterm_stopped = read_json("status-sigterm-stopped.json")
guest_fail = read_json("supervise-guest-fail.json")
guest_fail_status = read_json("status-guest-fail-final.json")
guest_fail_result = read_json("result-guest-fail.json")
helper_stopped = read_json("status-vsock-helper-stopped.json")

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
# The CLI `stop` alias records `halted` on a clean exit (not `stopped`);
# ShouldRestart treats `halted` the same as `stopped` for the `always`
# policy, so the restart trigger below is unaffected by the label change.
if always.get("final_state") != "halted":
    raise SystemExit(always)
# Both intentional stops must have been classified as clean restarts: each one
# brings the workspace back to `running` rather than tripping a terminal
# `failed`, which is the regression this sub-test guards.
for status in (always_running_1, always_running_2):
    if status.get("event", {}).get("state") != "running":
        raise SystemExit(status)
if always_status.get("event", {}).get("state") != "halted":
    raise SystemExit(always_status)
if cancel_after_kill.get("event", {}).get("state") != "running":
    raise SystemExit(cancel_after_kill)
if cancel_stopped.get("event", {}).get("state") != "halted":
    raise SystemExit(cancel_stopped)
if cancel_no_restart.get("event", {}).get("state") != "halted":
    raise SystemExit(cancel_no_restart)
for status in (sigint_after_signal, sigterm_after_signal):
    if status.get("event", {}).get("state") != "running":
        raise SystemExit(status)
for status in (sigint_stopped, sigterm_stopped, helper_stopped):
    if status.get("event", {}).get("state") != "halted":
        raise SystemExit(status)
if guest_fail.get("policy") != "on-failure" or guest_fail.get("restarts") != 2 or guest_fail.get("stopped") is not True:
    raise SystemExit(guest_fail)
if guest_fail.get("final_state") != "failed":
    raise SystemExit(guest_fail)
if guest_fail_status.get("event", {}).get("state") != "failed":
    raise SystemExit(guest_fail_status)
if guest_fail_result.get("result", {}).get("exitCode") != 42:
    raise SystemExit(guest_fail_result)
if "supervise-real-failure" not in guest_fail_result.get("result", {}).get("stdout", ""):
    raise SystemExit(guest_fail_result)
PY

for workspace in supervise-never supervise-on-failure supervise-always supervise-cancel supervise-sigint supervise-sigterm supervise-guest-fail; do
  "$CLI" delete "$workspace" --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete-${workspace}.json" || true
done

echo "microagent E2E supervision passed"
