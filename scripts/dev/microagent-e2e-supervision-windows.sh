#!/usr/bin/env bash
set -euo pipefail

# windows-hyperv arm of the supervision contract. The in-process supervisor
# means there is no --supervisor path and no host runtime PID (HCS owns the VM
# worker process), so the cancel contract is expressed as "the workspace keeps
# running after the supervise loop is killed" rather than "the runtime pid
# survives". The rootfs is a VHD (no mke2fs). Restarts are driven by a guest
# `poweroff -f` over the connect channel, mirroring the applevf arm.
#
# Contract coverage:
#   - never:      a never-restart workspace is not restarted; final stopped.
#   - always:     a guest poweroff is restarted up to --max-restarts, then the
#                 supervise loop exits with the workspace stopped.
#   - cancel:     killing only the supervise process leaves the workspace
#                 running (the VM is independent of the loop); a later stop
#                 brings it down with no policy-driven restart.
#   - guest-fail: an on-failure workspace whose service exits non-zero is
#                 restarted to the cap, ends failed, and the result carries the
#                 guest exit code and stdout.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

e2e_is_windows || e2e_skip "windows-hyperv supervision E2E requires a Windows host"
e2e_have_hcs || e2e_skip "Hyper-V HCS services (vmms/vmcompute) are not running"
for required in go python3; do
  e2e_require_cmd "$required" "$required is required for windows-hyperv supervision E2E"
done

IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox:1.36}"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-supervision-whv.XXXXXX")"
CLI="$STATE_DIR/microagent.exe"
GUEST_INIT="$STATE_DIR/microagent-guestinit"
KERNEL="$HOME/.microagent/kernels/windows-hyperv/amd64/Image"
SIZE_MIB=512
NEVER_WS="whv-supervise-never"
ALWAYS_WS="whv-supervise-always"
CANCEL_WS="whv-supervise-cancel"
GUEST_FAIL_WS="whv-supervise-guest-fail"
ALWAYS_SUPERVISE_PID=""
CANCEL_SUPERVISE_PID=""

cleanup() {
  status="$?"
  for pid in "$ALWAYS_SUPERVISE_PID" "$CANCEL_SUPERVISE_PID"; do
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  done
  if [ -x "$CLI" ]; then
    for ws in "$NEVER_WS" "$ALWAYS_WS" "$CANCEL_WS" "$GUEST_FAIL_WS"; do
      "$CLI" kill "$ws" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_SUPERVISION:-0}" != "1" ]; then
        "$CLI" delete "$ws" --yes --force --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      fi
    done
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_SUPERVISION:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E supervision windows-hyperv state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

cd "$ROOT"
export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
if [ -z "${DOCKER_CONFIG:-}" ]; then
  mkdir -p "$STATE_DIR/docker-config"
  export DOCKER_CONFIG="$STATE_DIR/docker-config"
fi

e2e_step "build CLI and guest init"
go build -buildvcs=false -o "$CLI" ./cmd/microagent
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
if [ ! -r "$KERNEL" ]; then
  "$CLI" kernel install || e2e_skip "windows-hyperv kernel install failed"
fi

create_ws() { # create_ws <name> <restart> <entrypoint>
  # Use --entrypoint (not --service-command): an entrypoint is a workload that
  # runs to completion and emits a structured result.json on exit, which the
  # guest-fail case asserts (exit code + stdout). A --service-command is a
  # persistent service with no terminal result, so it cannot express the
  # on-failure result contract. This mirrors the applevf supervision arm, which
  # also drives every policy through --entrypoint.
  "$CLI" create "$1" --image "$IMAGE" --network isolated --restart "$2" \
    --entrypoint "$3" --guest-init "$GUEST_INIT" --state-dir "$STATE_DIR" \
    --size-mib "$SIZE_MIB"
}

ws_state() { # ws_state <name>: print the latest lifecycle state
  "$CLI" --json status "$1" --state-dir "$STATE_DIR" 2>/dev/null \
    | python3 -c 'import json,sys; print((json.load(sys.stdin).get("event") or {}).get("state",""))'
}

wait_state() { # wait_state <name> <state> <timeout>
  ws="$1"; want="$2"; deadline="$((SECONDS + ${3:-60}))"
  while true; do
    [ "$(ws_state "$ws")" = "$want" ] && return 0
    [ "$SECONDS" -ge "$deadline" ] && { echo "workspace $ws did not reach $want (last=$(ws_state "$ws"))" >&2; return 1; }
    sleep 1
  done
}

wait_running_ready() { # wait_running_ready <name> <timeout>
  ws="$1"; deadline="$((SECONDS + ${2:-60}))"
  while true; do
    if "$CLI" --json status "$ws" --state-dir "$STATE_DIR" 2>/dev/null \
      | python3 -c 'import json,sys; s=json.load(sys.stdin); r=s.get("readiness") or {}; sys.exit(0 if (s.get("event") or {}).get("state")=="running" and (r.get("execReady") or {}).get("ready") and (r.get("shellReady") or {}).get("ready") else 1)'; then
      return 0
    fi
    [ "$SECONDS" -ge "$deadline" ] && { echo "workspace $ws never became running+ready" >&2; return 1; }
    sleep 1
  done
}

# --- never: a never-restart workspace is not restarted ---
e2e_step "never policy: no restart, final stopped"
create_ws "$NEVER_WS" never "sleep 300" >"$STATE_DIR/create-never.json"
"$CLI" --json supervise "$NEVER_WS" --state-dir "$STATE_DIR" --interval 1 --max-restarts 1 \
  >"$STATE_DIR/supervise-never.json" 2>&1 || { cat "$STATE_DIR/supervise-never.json"; e2e_fail "supervise never"; }
python3 - "$STATE_DIR/supervise-never.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
if d.get("policy") != "never" or d.get("restarts") != 0 or d.get("stopped") is not True:
    raise SystemExit(f"never result unexpected: {d}")
PY

# --- always: a guest poweroff is restarted to the cap, then exits stopped ---
e2e_step "always policy: guest poweroff triggers restart"
create_ws "$ALWAYS_WS" always "sleep 300" >"$STATE_DIR/create-always.json"
"$CLI" --json supervise "$ALWAYS_WS" --state-dir "$STATE_DIR" --interval 1 --max-restarts 1 \
  >"$STATE_DIR/supervise-always.json" 2>"$STATE_DIR/supervise-always.err" &
ALWAYS_SUPERVISE_PID=$!
wait_running_ready "$ALWAYS_WS" 90 || e2e_fail "always workspace never became ready"
# Power the guest off from inside; supervise must observe the stop and restart.
"$CLI" connect "$ALWAYS_WS" --state-dir "$STATE_DIR" --send "poweroff -f" \
  --ready-timeout 45 --timeout 10 >"$STATE_DIR/poweroff-always.txt" 2>&1 || true
# The supervise loop runs one restart, then halts at the cap and exits.
supervise_wait=0
while kill -0 "$ALWAYS_SUPERVISE_PID" 2>/dev/null; do
  if [ "$supervise_wait" -ge 120 ]; then
    kill "$ALWAYS_SUPERVISE_PID" 2>/dev/null || true
    wait "$ALWAYS_SUPERVISE_PID" 2>/dev/null || true
    cat "$STATE_DIR/supervise-always.json" "$STATE_DIR/supervise-always.err" >&2
    e2e_fail "always supervise did not exit after restart cap"
  fi
  sleep 1
  supervise_wait=$((supervise_wait + 1))
done
wait "$ALWAYS_SUPERVISE_PID" || { cat "$STATE_DIR/supervise-always.json" "$STATE_DIR/supervise-always.err" >&2; e2e_fail "always supervise exited non-zero"; }
ALWAYS_SUPERVISE_PID=""
python3 - "$STATE_DIR/supervise-always.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
if d.get("policy") != "always" or d.get("restarts", 0) < 1 or d.get("stopped") is not True:
    raise SystemExit(f"always result unexpected: {d}")
if d.get("final_state") != "stopped":
    raise SystemExit(f"always final_state != stopped: {d}")
PY

# --- cancel: killing only the supervise process leaves the VM running ---
e2e_step "cancel: killing supervise leaves the workspace running"
create_ws "$CANCEL_WS" always "sleep 300" >"$STATE_DIR/create-cancel.json"
"$CLI" supervise "$CANCEL_WS" --state-dir "$STATE_DIR" --interval 1 --max-restarts 0 \
  >"$STATE_DIR/supervise-cancel.json" 2>&1 &
CANCEL_SUPERVISE_PID=$!
wait_running_ready "$CANCEL_WS" 90 || e2e_fail "cancel workspace never became ready"
kill "$CANCEL_SUPERVISE_PID" 2>/dev/null || true
wait "$CANCEL_SUPERVISE_PID" 2>/dev/null || true
CANCEL_SUPERVISE_PID=""
# The VM is owned by HCS, not the supervise loop: it must still be running.
sleep 2
[ "$(ws_state "$CANCEL_WS")" = "running" ] || e2e_fail "workspace stopped when only the supervise loop was killed"
# A direct stop brings it down with no policy-driven restart.
"$CLI" stop "$CANCEL_WS" --state-dir "$STATE_DIR" >"$STATE_DIR/stop-cancel.json" 2>&1 || true
wait_state "$CANCEL_WS" stopped 60 || e2e_fail "cancel workspace did not stop"
sleep 3
[ "$(ws_state "$CANCEL_WS")" = "stopped" ] || e2e_fail "cancel workspace restarted after a manual stop"

# --- guest-fail: on-failure restarts to the cap and ends failed ---
e2e_step "guest-fail: on-failure restarts to the cap, ends failed"
create_ws "$GUEST_FAIL_WS" on-failure "printf supervise-real-failure; exit 42" \
  >"$STATE_DIR/create-guest-fail.json"
"$CLI" --json supervise "$GUEST_FAIL_WS" --state-dir "$STATE_DIR" --interval 1 --max-restarts 2 \
  >"$STATE_DIR/supervise-guest-fail.json" 2>&1 || true
python3 - "$STATE_DIR/supervise-guest-fail.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
if d.get("policy") != "on-failure" or d.get("restarts") != 2 or d.get("stopped") is not True:
    raise SystemExit(f"guest-fail result unexpected: {d}")
if d.get("final_state") != "failed":
    raise SystemExit(f"guest-fail final_state != failed: {d}")
PY
"$CLI" --json result "$GUEST_FAIL_WS" --state-dir "$STATE_DIR" >"$STATE_DIR/result-guest-fail.json" 2>&1 || true
python3 - "$STATE_DIR/result-guest-fail.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
res = d.get("result") or {}
if res.get("exitCode") != 42:
    raise SystemExit(f"guest-fail exit code != 42: {d}")
if "supervise-real-failure" not in (res.get("stdout") or ""):
    raise SystemExit(f"guest-fail stdout missing marker: {d}")
PY

echo "microagent E2E supervision passed for windows-hyperv"
