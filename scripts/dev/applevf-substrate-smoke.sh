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
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-applevf-substrate.XXXXXX")"
WORKSPACE="substrate-smoke"
CLI="$STATE_DIR/microagent"
GUEST_INIT="$STATE_DIR/microagent-guestinit"
ARTIFACT_DIR="$STATE_DIR/artifacts"

cleanup() {
  status="$?"
  "$CLI" kill "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_APPLEVF_SUBSTRATE_SMOKE:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept Apple VF substrate smoke state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Darwin:arm64)
    ;;
  *)
    e2e_skip "Apple VF substrate smoke requires macOS on Apple silicon"
    ;;
esac

if [ ! -r "$KERNEL" ]; then
  e2e_skip "kernel is not readable at $KERNEL"
fi
if [ ! -x "$SUPERVISOR" ]; then
  e2e_skip "supervisor is not executable at $SUPERVISOR; run scripts/dev/applevf-supervisor-build.sh"
fi
export MICROAGENT_APPLEVF_SUPERVISOR="$SUPERVISOR"

if command -v mke2fs >/dev/null 2>&1; then
  MKE2FS="$(command -v mke2fs)"
elif [ -x /opt/homebrew/opt/e2fsprogs/sbin/mke2fs ]; then
  MKE2FS="/opt/homebrew/opt/e2fsprogs/sbin/mke2fs"
else
  e2e_skip "mke2fs not found; install e2fsprogs"
fi

if command -v debugfs >/dev/null 2>&1; then
  DEBUGFS="$(command -v debugfs)"
elif [ -x /opt/homebrew/opt/e2fsprogs/sbin/debugfs ]; then
  DEBUGFS="/opt/homebrew/opt/e2fsprogs/sbin/debugfs"
else
  e2e_skip "debugfs not found; install e2fsprogs"
fi

wait_for_status_ready() {
  local output="$1"
  local deadline="$((SECONDS + 30))"
  while true; do
    "$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$output"
    if python3 - "$output" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    status = json.load(handle)
event = status.get("event") or {}
readiness = status.get("readiness") or {}
if event.get("state") == "running" and readiness.get("guestReady", {}).get("ready") and readiness.get("shellReady", {}).get("ready"):
    raise SystemExit(0)
raise SystemExit(1)
PY
    then
      return 0
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "Apple VF workspace $WORKSPACE did not become ready" >&2
      cat "$output" >&2
      return 1
    fi
    sleep 1
  done
}

(
  cd "$ROOT"
  go build -o "$CLI" ./cmd/microagent
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

"$CLI" create "$WORKSPACE" \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --size-mib "${MICROAGENT_APPLEVF_BOOT_SIZE_MIB:-128}" \
  --mke2fs "$MKE2FS" \
  --kernel "$KERNEL" \
  --state-dir "$STATE_DIR" \
  --memory "${MICROAGENT_APPLEVF_BOOT_MEMORY_MIB:-512}" \
  --cpus "${MICROAGENT_APPLEVF_BOOT_CPUS:-2}" \
  --timeout "${MICROAGENT_APPLEVF_BOOT_TIMEOUT_SECONDS:-30}" \
  --guest-init "$GUEST_INIT" \
  --supervisor "$SUPERVISOR" \
  --result-port 1024 \
  --output report=/report.json >"$STATE_DIR/create.json"

"$CLI" start "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" >"$STATE_DIR/start.json"
wait_for_status_ready "$STATE_DIR/status-running.json"

"$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "printf substrate-live > /preserved.txt; printf '{\"ok\":true,\"phase\":\"running\"}' > /report.json; sync" \
  --timeout 10 >"$STATE_DIR/write.txt"
"$CLI" artifacts "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/artifacts.json"
"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt.json"
"$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-halted.json"
mkdir -p "$ARTIFACT_DIR/running"
"$CLI" artifacts get "$WORKSPACE" report "$ARTIFACT_DIR/running" \
  --state-dir "$STATE_DIR" \
  --debugfs "$DEBUGFS" >"$STATE_DIR/artifact-get-running.json"

"$CLI" start "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" >"$STATE_DIR/resume.json"
wait_for_status_ready "$STATE_DIR/status-resumed.json"
"$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "cat /preserved.txt; printf '{\"ok\":true,\"phase\":\"resumed\"}' > /report.json; sync" \
  --timeout 10 >"$STATE_DIR/resume-read.txt"
"$CLI" quarantine "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/quarantine.json"
"$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-quarantined.json"
if "$CLI" start "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" >"$STATE_DIR/start-quarantined.json" 2>"$STATE_DIR/start-quarantined.err"; then
  echo "start succeeded while Apple VF workspace was quarantined" >&2
  exit 1
fi
"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt-quarantined.json"
mkdir -p "$ARTIFACT_DIR/resumed"
"$CLI" artifacts get "$WORKSPACE" report "$ARTIFACT_DIR/resumed" \
  --state-dir "$STATE_DIR" \
  --debugfs "$DEBUGFS" >"$STATE_DIR/artifact-get-resumed.json"

python3 - "$STATE_DIR" "$WORKSPACE" <<'PY'
import json
import os
import sys

state_dir, workspace = sys.argv[1:]

def read_json(name):
    with open(os.path.join(state_dir, name), "r", encoding="utf-8") as handle:
        return json.load(handle)

create = read_json("create.json")
start = read_json("start.json")
running = read_json("status-running.json")
artifacts = read_json("artifacts.json")
artifact_running = read_json("artifact-get-running.json")
halt = read_json("halt.json")
halted = read_json("status-halted.json")
resume = read_json("resume.json")
resumed = read_json("status-resumed.json")
artifact_resumed = read_json("artifact-get-resumed.json")
quarantine = read_json("quarantine.json")
quarantined = read_json("status-quarantined.json")

if create["response"]["event"]["state"] != "prepared":
    raise SystemExit(create)
if start["response"]["event"]["state"] != "running":
    raise SystemExit(start)
if running["event"]["state"] != "running":
    raise SystemExit(running)
if not running["verification"]["ok"]:
    raise SystemExit(running)
if not running["readiness"]["guestReady"]["ready"] or not running["readiness"]["shellReady"]["ready"]:
    raise SystemExit(running)
if artifacts["artifacts"]["egress"][0]["name"] != "report":
    raise SystemExit(artifacts)
if artifact_running["artifact"] != "report" or artifact_running["disk"] != "rootfs":
    raise SystemExit(artifact_running)
with open(os.path.join(state_dir, "artifacts", "running", "report.json"), "r", encoding="utf-8") as handle:
    if json.load(handle) != {"ok": True, "phase": "running"}:
        raise SystemExit("running artifact mismatch")
if halt["event"]["state"] != "halted" or halted["event"]["state"] != "halted":
    raise SystemExit(halted)
if resume["response"]["event"]["state"] != "running" or resumed["event"]["state"] != "running":
    raise SystemExit(resumed)
with open(os.path.join(state_dir, "resume-read.txt"), "r", encoding="utf-8", errors="replace") as handle:
    if "substrate-live" not in handle.read():
        raise SystemExit("preserved rootfs marker was not visible after resume")
if artifact_resumed["artifact"] != "report" or artifact_resumed["disk"] != "rootfs":
    raise SystemExit(artifact_resumed)
with open(os.path.join(state_dir, "artifacts", "resumed", "report.json"), "r", encoding="utf-8") as handle:
    if json.load(handle) != {"ok": True, "phase": "resumed"}:
        raise SystemExit("resumed artifact mismatch")
if quarantine["event"]["state"] != "quarantined" or quarantined["event"]["state"] != "quarantined":
    raise SystemExit(quarantined)
if quarantined["readiness"]["guestReady"]["ready"] is not True:
    raise SystemExit(quarantined)
with open(os.path.join(state_dir, workspace, "events.json"), "r", encoding="utf-8") as handle:
    states = [event["state"] for event in json.load(handle)]
for expected in ("prepared", "running", "halted", "quarantined"):
    if expected not in states:
        raise SystemExit(states)
if states.count("running") < 2:
    raise SystemExit(states)
with open(os.path.join(state_dir, "start-quarantined.err"), "r", encoding="utf-8", errors="replace") as handle:
    if "quarantined" not in handle.read():
        raise SystemExit("start from quarantined state did not report quarantine")
PY

"$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete.json"
python3 - "$STATE_DIR/delete.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    delete = json.load(handle)
if delete["event"]["state"] != "stopped":
    raise SystemExit(delete)
PY

echo "Apple VF substrate smoke passed"
