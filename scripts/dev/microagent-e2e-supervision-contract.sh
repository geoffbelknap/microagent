#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

default_backend() {
  case "$(uname -s):$(uname -m)" in
    Linux:x86_64|Linux:amd64)
      printf '%s\n' linux-kvm
      ;;
    Darwin:arm64)
      printf '%s\n' applevf
      ;;
    MINGW*:x86_64|MSYS*:x86_64|CYGWIN*:x86_64)
      printf '%s\n' windows-hyperv
      ;;
    *)
      printf '%s\n' unsupported
      ;;
  esac
}

BACKEND="${MICROAGENT_E2E_BACKEND:-$(default_backend)}"

if [ "$BACKEND" = "linux-kvm" ]; then
  exec "$ROOT/scripts/dev/microagent-e2e-supervision.sh"
fi

if [ "$BACKEND" = "windows-hyperv" ]; then
  exec "$ROOT/scripts/dev/microagent-e2e-supervision-windows.sh"
fi

if [ "$BACKEND" != "applevf" ]; then
  e2e_skip "microagent supervision E2E does not support backend lane: $BACKEND"
fi

case "$(uname -s):$(uname -m)" in
  Darwin:arm64)
    ;;
  *)
    e2e_skip "Apple VF supervision E2E requires macOS on Apple silicon"
    ;;
esac

SUPERVISOR="${MICROAGENT_APPLEVF_SUPERVISOR:-$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor}"
KERNEL="${MICROAGENT_APPLEVF_KERNEL:-$HOME/.microagent/kernels/apple-vf/arm64/Image}"
if [ ! -r "$KERNEL" ] && [ -r "$HOME/.microagent/kernels/apple-vf/Image" ]; then
  KERNEL="$HOME/.microagent/kernels/apple-vf/Image"
fi
IMAGE="${MICROAGENT_APPLEVF_BOOT_IMAGE:-docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6}"
ARCH="${MICROAGENT_APPLEVF_BOOT_ARCH:-arm64}"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-supervision-applevf.XXXXXX")"
CLI="$STATE_DIR/microagent"
GUEST_INIT="$STATE_DIR/microagent-guestinit"
SUPERVISE_PID=""
CANCEL_SUPERVISE_PID=""
SIGNAL_SUPERVISE_PID=""
HELPER_SUPERVISE_PID=""

cleanup() {
  status="$?"
  for pid in "$SUPERVISE_PID" "$CANCEL_SUPERVISE_PID" "$SIGNAL_SUPERVISE_PID" "$HELPER_SUPERVISE_PID"; do
    if [ -n "$pid" ] && ps -p "$pid" >/dev/null 2>&1; then
      kill "$pid" >/dev/null 2>&1 || true
      wait "$pid" 2>/dev/null || true
    fi
  done
  if [ -x "$CLI" ]; then
    "$CLI" kill supervise-always --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" kill supervise-cancel --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" kill supervise-sigint --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" kill supervise-sigterm --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" kill supervise-start-fail --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" kill supervise-guest-fail --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" kill supervise-mediation-helper --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_SUPERVISION:-0}" != "1" ]; then
      for workspace in supervise-never supervise-always supervise-cancel supervise-sigint supervise-sigterm supervise-start-fail supervise-guest-fail supervise-mediation-helper; do
        "$CLI" delete "$workspace" --yes --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
      done
    fi
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_SUPERVISION:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E supervision Apple VF state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

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
    with open(sys.argv[1], "r", encoding="utf-8") as handle:
        status = json.load(handle)
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
  ) >"$output" 2>"$output.err" &
  STARTED_SUPERVISE_PID="$!"
}

read_runtime_field() {
  workspace="$1"
  field="$2"
  output="$3"
  python3 - "$STATE_DIR/$workspace/runtime.json" "$field" "$output" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    runtime = json.load(handle)
value = runtime.get(sys.argv[2]) or 0
if value <= 0:
    raise SystemExit(runtime)
with open(sys.argv[3], "w", encoding="utf-8") as handle:
    handle.write(str(value))
PY
}

signal_supervise_process() {
  workspace="$1"
  signal="$2"
  prefix="$3"
  create_workspace "$workspace" always "sleep 300" >"$STATE_DIR/create-$prefix.json"
  start_supervise_background "$workspace" "$STATE_DIR/supervise-$prefix.json" \
    --backend apple-vf \
    --state-dir "$STATE_DIR" \
    --kernel "$KERNEL" \
    --supervisor "$SUPERVISOR" \
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
  "$CLI" stop "$workspace" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/stop-$prefix.json"
  wait_for_state "$workspace" halted "$STATE_DIR/status-$prefix-stopped.json"
  wait_for_process_exit "$runtime_pid"
}

create_workspace() {
  workspace="$1"
  restart="$2"
  entrypoint="$3"
  shift 3
  "$CLI" create "$workspace" \
    --backend apple-vf \
    --image "$IMAGE" \
    --arch "$ARCH" \
    --kernel "$KERNEL" \
    --guest-init "$GUEST_INIT" \
    --supervisor "$SUPERVISOR" \
    --state-dir "$STATE_DIR" \
    --size-mib "${MICROAGENT_APPLEVF_BOOT_SIZE_MIB:-128}" \
    --mke2fs "$MKE2FS" \
    --network isolated \
    --restart "$restart" \
    --entrypoint "$entrypoint" \
    "$@"
}

(
  cd "$ROOT"
  go build -buildvcs=false -o "$CLI" ./cmd/microagent
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

"$CLI" doctor --backend apple-vf --arch "$ARCH" --supervisor "$SUPERVISOR" >"$STATE_DIR/doctor.json"

create_workspace supervise-never never "sleep 300" >"$STATE_DIR/create-never.json"
"$CLI" supervise supervise-never \
  --backend apple-vf \
  --state-dir "$STATE_DIR" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" \
  --interval 1 \
  --max-restarts 1 >"$STATE_DIR/supervise-never.json"
"$CLI" status supervise-never --state-dir "$STATE_DIR" >"$STATE_DIR/status-never.json"

create_workspace supervise-start-fail on-failure "sleep 300" >"$STATE_DIR/create-start-fail.json"
"$CLI" supervise supervise-start-fail \
  --backend apple-vf \
  --state-dir "$STATE_DIR" \
  --kernel "$STATE_DIR/missing-kernel" \
  --supervisor "$SUPERVISOR" \
  --interval 1 \
  --max-restarts 2 >"$STATE_DIR/supervise-start-fail.json"
"$CLI" status supervise-start-fail --state-dir "$STATE_DIR" >"$STATE_DIR/status-start-fail-final.json" || true

create_workspace supervise-always always "sleep 300" >"$STATE_DIR/create-always.json"
start_supervise_background supervise-always "$STATE_DIR/supervise-always.json" \
  --backend apple-vf \
  --state-dir "$STATE_DIR" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" \
  --interval 1 \
  --max-restarts 2
SUPERVISE_PID="$STARTED_SUPERVISE_PID"
wait_for_state supervise-always running "$STATE_DIR/status-always-running-1.json"
"$CLI" connect supervise-always \
  --state-dir "$STATE_DIR" \
  --send "poweroff -f" \
  --ready-timeout 30 \
  --timeout 10 >"$STATE_DIR/poweroff-always-1.txt" || true
wait_for_state supervise-always running "$STATE_DIR/status-always-running-2.json"
"$CLI" connect supervise-always \
  --state-dir "$STATE_DIR" \
  --send "poweroff -f" \
  --ready-timeout 30 \
  --timeout 10 >"$STATE_DIR/poweroff-always-2.txt" || true
wait "$SUPERVISE_PID"
SUPERVISE_PID=""
"$CLI" status supervise-always --state-dir "$STATE_DIR" >"$STATE_DIR/status-always-final.json"

create_workspace supervise-cancel always "sleep 300" >"$STATE_DIR/create-cancel.json"
start_supervise_background supervise-cancel "$STATE_DIR/supervise-cancel.json" \
  --backend apple-vf \
  --state-dir "$STATE_DIR" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" \
  --interval 1 \
  --max-restarts 0
CANCEL_SUPERVISE_PID="$STARTED_SUPERVISE_PID"
wait_for_state supervise-cancel running "$STATE_DIR/status-cancel-running.json"
read_runtime_field supervise-cancel pid "$STATE_DIR/cancel-runtime-pid.txt"
cancel_runtime_pid="$(cat "$STATE_DIR/cancel-runtime-pid.txt")"
kill "$CANCEL_SUPERVISE_PID"
wait_for_process_exit "$CANCEL_SUPERVISE_PID"
wait "$CANCEL_SUPERVISE_PID" || true
CANCEL_SUPERVISE_PID=""
"$CLI" status supervise-cancel --state-dir "$STATE_DIR" >"$STATE_DIR/status-cancel-after-supervise-kill.json" || true
if ! process_is_active "$cancel_runtime_pid"; then
  echo "workspace runtime pid $cancel_runtime_pid exited when only the supervise process was killed" >&2
  exit 1
fi
# The supervise loop was already killed above (no restart policy left to
# exercise); this is a direct park/cleanup check, so the CLI `stop` alias's
# clean-exit state is `halted`, not `stopped`.
"$CLI" stop supervise-cancel --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/stop-cancel.json"
wait_for_state supervise-cancel halted "$STATE_DIR/status-cancel-stopped.json"
wait_for_process_exit "$cancel_runtime_pid"
sleep 3
"$CLI" status supervise-cancel --state-dir "$STATE_DIR" >"$STATE_DIR/status-cancel-no-restart.json" || true

signal_supervise_process supervise-sigint INT sigint
signal_supervise_process supervise-sigterm TERM sigterm

create_workspace supervise-guest-fail on-failure "printf supervise-real-failure; exit 42" >"$STATE_DIR/create-guest-fail.json"
"$CLI" supervise supervise-guest-fail \
  --backend apple-vf \
  --state-dir "$STATE_DIR" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" \
  --interval 1 \
  --max-restarts 2 >"$STATE_DIR/supervise-guest-fail.json"
"$CLI" status supervise-guest-fail --state-dir "$STATE_DIR" >"$STATE_DIR/status-guest-fail-final.json" || true
"$CLI" result supervise-guest-fail --state-dir "$STATE_DIR" >"$STATE_DIR/result-guest-fail.json" || true

create_workspace supervise-mediation-helper always "sleep 300" \
  --mediation "2050=127.0.0.1:9" \
  --mediation-optional >"$STATE_DIR/create-mediation-helper.json"
start_supervise_background supervise-mediation-helper "$STATE_DIR/supervise-mediation-helper.json" \
  --backend apple-vf \
  --state-dir "$STATE_DIR" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" \
  --interval 1 \
  --max-restarts 0
HELPER_SUPERVISE_PID="$STARTED_SUPERVISE_PID"
wait_for_state supervise-mediation-helper running "$STATE_DIR/status-mediation-helper-running.json"
read_runtime_field supervise-mediation-helper pid "$STATE_DIR/mediation-helper-runtime-pid.txt"
helper_runtime_pid="$(cat "$STATE_DIR/mediation-helper-runtime-pid.txt")"
kill -TERM "$HELPER_SUPERVISE_PID"
wait "$HELPER_SUPERVISE_PID" || true
HELPER_SUPERVISE_PID=""
# The supervise loop was already killed above (max-restarts 0); this is a
# direct park/cleanup check, so the CLI `stop` alias's clean-exit state is
# `halted`, not `stopped`.
"$CLI" stop supervise-mediation-helper --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/stop-mediation-helper.json"
wait_for_state supervise-mediation-helper halted "$STATE_DIR/status-mediation-helper-stopped.json"
wait_for_process_exit "$helper_runtime_pid"
"$CLI" delete supervise-mediation-helper --yes --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/delete-mediation-helper.json"
if [ -e "$STATE_DIR/supervise-mediation-helper/runtime.json" ]; then
  echo "Apple VF mediation helper runtime state leaked after delete" >&2
  exit 1
fi

python3 - "$STATE_DIR" <<'PY'
import json
import os
import sys

state_dir = sys.argv[1]

def read_json(name):
    with open(os.path.join(state_dir, name), "r", encoding="utf-8") as handle:
        return json.load(handle)

doctor = read_json("doctor.json")
never = read_json("supervise-never.json")
never_status = read_json("status-never.json")
start_fail = read_json("supervise-start-fail.json")
start_fail_status = read_json("status-start-fail-final.json")
always = read_json("supervise-always.json")
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
helper_running = read_json("status-mediation-helper-running.json")
helper_stopped = read_json("status-mediation-helper-stopped.json")
helper_delete = read_json("delete-mediation-helper.json")

if doctor.get("ok") is not True or doctor.get("backend") != "apple-vf":
    raise SystemExit(doctor)
if never.get("policy") != "never" or never.get("restarts") != 0 or never.get("stopped") is not True:
    raise SystemExit(never)
if never_status.get("event", {}).get("state") not in ("prepared", "stopped"):
    raise SystemExit(never_status)
if start_fail.get("policy") != "on-failure" or start_fail.get("restarts") != 2 or start_fail.get("stopped") is not True:
    raise SystemExit(start_fail)
if start_fail.get("final_state") != "failed":
    raise SystemExit(start_fail)
if start_fail_status.get("event", {}).get("state") != "failed":
    raise SystemExit(start_fail_status)
if always.get("policy") != "always" or always.get("restarts") != 2 or always.get("stopped") is not True:
    raise SystemExit(always)
if always.get("final_state") != "stopped":
    raise SystemExit(always)
if always_status.get("event", {}).get("state") != "stopped":
    raise SystemExit(always_status)
if cancel_after_kill.get("event", {}).get("state") != "running":
    raise SystemExit(cancel_after_kill)
# These three come from the CLI `stop` alias, which records `halted` on a
# clean exit (not `stopped`); no restart policy is exercised on this path.
if cancel_stopped.get("event", {}).get("state") not in ("halted", "stopped"):
    raise SystemExit(cancel_stopped)
if cancel_no_restart.get("event", {}).get("state") not in ("halted", "stopped"):
    raise SystemExit(cancel_no_restart)
for status in (sigint_after_signal, sigterm_after_signal):
    if status.get("event", {}).get("state") != "running":
        raise SystemExit(status)
for status in (sigint_stopped, sigterm_stopped):
    if status.get("event", {}).get("state") not in ("halted", "stopped"):
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
if helper_running.get("event", {}).get("state") != "running":
    raise SystemExit(helper_running)
# CLI `stop` alias again: clean exit records `halted`, not `stopped`.
if helper_stopped.get("event", {}).get("state") not in ("halted", "stopped"):
    raise SystemExit(helper_stopped)
if helper_delete.get("event", {}).get("state") != "stopped":
    raise SystemExit(helper_delete)
PY

for workspace in supervise-never supervise-start-fail supervise-always supervise-cancel supervise-sigint supervise-sigterm supervise-guest-fail supervise-mediation-helper; do
  "$CLI" delete "$workspace" --yes --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/delete-${workspace}.json" || true
done

echo "microagent E2E supervision passed for applevf"
