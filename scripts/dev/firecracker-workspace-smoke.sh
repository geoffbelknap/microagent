#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-firecracker-workspace.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
CONNECT_RESULT="$STATE_DIR/connect.txt"
ARTIFACT_DIR="$STATE_DIR/artifacts"
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
    e2e_skip "firecracker workspace smoke requires Linux amd64"
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

wait_for_status_ready() {
  workspace="$1"
  state_dir="$2"
  output="$3"
  deadline="$((SECONDS + 30))"
  while true; do
    "$CLI" status "$workspace" --state-dir "$state_dir" >"$output"
    if python3 - "$output" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    status = json.load(f)
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
      echo "workspace $workspace did not become ready" >&2
      cat "$output" >&2
      return 1
    fi
    sleep 1
  done
}

(
  cd "$ROOT"
  go build -buildvcs=false -o "$CLI" ./cmd/microagent
  go build -buildvcs=false -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
  (
    export GOOS=linux
    export GOARCH=amd64
    export CGO_ENABLED=0
    go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
  )
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
if result["ok"] is not True:
    raise SystemExit(result)
if result["backend"] != "firecracker":
    raise SystemExit(result)
if result["host"]["kvmAvailable"] is not True:
    raise SystemExit(result)
if result["host"]["vsockAvailable"] is not True:
    raise SystemExit(result)
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
grep -q "console input is unavailable in state prepared" "$STATE_DIR/connect.err"
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

"$CLI" create substrate-smoke \
  --image "$IMAGE" \
  --arch amd64 \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR/substrate" \
  --size-mib 128 \
  --result-port 1024 \
  --output report=/report.json >"$STATE_DIR/substrate-create.json"

"$CLI" start substrate-smoke --state-dir "$STATE_DIR/substrate" --kernel "$kernel_path" >"$STATE_DIR/substrate-start.json"
wait_for_status_ready substrate-smoke "$STATE_DIR/substrate" "$STATE_DIR/substrate-status-running.json"
"$CLI" connect substrate-smoke --state-dir "$STATE_DIR/substrate" --send "printf substrate-live > /preserved.txt; printf '{\"ok\":true,\"phase\":\"running\"}' > /report.json; sync" --timeout 5 >"$STATE_DIR/substrate-write.txt"
"$CLI" artifacts substrate-smoke --state-dir "$STATE_DIR/substrate" >"$STATE_DIR/substrate-artifacts.json"
"$CLI" halt substrate-smoke --state-dir "$STATE_DIR/substrate" >"$STATE_DIR/substrate-halt.json"
"$CLI" status substrate-smoke --state-dir "$STATE_DIR/substrate" >"$STATE_DIR/substrate-status-halted.json"
mkdir -p "$ARTIFACT_DIR/running"
"$CLI" artifacts get substrate-smoke report "$ARTIFACT_DIR/running" --state-dir "$STATE_DIR/substrate" >"$STATE_DIR/substrate-artifact-get-running.json"
"$CLI" start substrate-smoke --state-dir "$STATE_DIR/substrate" --kernel "$kernel_path" >"$STATE_DIR/substrate-resume.json"
wait_for_status_ready substrate-smoke "$STATE_DIR/substrate" "$STATE_DIR/substrate-status-resumed.json"
"$CLI" connect substrate-smoke --state-dir "$STATE_DIR/substrate" --send "cat /preserved.txt; printf '{\"ok\":true,\"phase\":\"resumed\"}' > /report.json; sync" --timeout 5 >"$STATE_DIR/substrate-resume-read.txt"
"$CLI" quarantine substrate-smoke --state-dir "$STATE_DIR/substrate" >"$STATE_DIR/substrate-quarantine.json"
"$CLI" status substrate-smoke --state-dir "$STATE_DIR/substrate" >"$STATE_DIR/substrate-status-quarantined.json"
if "$CLI" start substrate-smoke --state-dir "$STATE_DIR/substrate" --kernel "$kernel_path" >"$STATE_DIR/substrate-start-quarantined.json" 2>"$STATE_DIR/substrate-start-quarantined.err"; then
  echo "start succeeded while Firecracker workspace was quarantined" >&2
  exit 1
fi
"$CLI" halt substrate-smoke --state-dir "$STATE_DIR/substrate" >"$STATE_DIR/substrate-halt-quarantined.json"
mkdir -p "$ARTIFACT_DIR/resumed"
"$CLI" artifacts get substrate-smoke report "$ARTIFACT_DIR/resumed" --state-dir "$STATE_DIR/substrate" >"$STATE_DIR/substrate-artifact-get-resumed.json"

python3 - "$STATE_DIR" <<'PY'
import json
import os
import sys

state_dir = sys.argv[1]

def read_json(name):
    with open(os.path.join(state_dir, name), "r", encoding="utf-8") as f:
        return json.load(f)

create = read_json("substrate-create.json")
start = read_json("substrate-start.json")
running = read_json("substrate-status-running.json")
artifacts = read_json("substrate-artifacts.json")
artifact_running = read_json("substrate-artifact-get-running.json")
halt = read_json("substrate-halt.json")
halted = read_json("substrate-status-halted.json")
resume = read_json("substrate-resume.json")
resumed = read_json("substrate-status-resumed.json")
artifact_resumed = read_json("substrate-artifact-get-resumed.json")
quarantine = read_json("substrate-quarantine.json")
quarantined = read_json("substrate-status-quarantined.json")

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
with open(os.path.join(state_dir, "artifacts", "running", "report.json"), "r", encoding="utf-8") as f:
    if json.load(f) != {"ok": True, "phase": "running"}:
        raise SystemExit("running artifact mismatch")
if halt["event"]["state"] != "halted" or halted["event"]["state"] != "halted":
    raise SystemExit(halted)
if resume["response"]["event"]["state"] != "running" or resumed["event"]["state"] != "running":
    raise SystemExit(resumed)
if "substrate-live" not in open(os.path.join(state_dir, "substrate-resume-read.txt"), "r", encoding="utf-8", errors="replace").read():
    raise SystemExit("preserved rootfs marker was not visible after resume")
if artifact_resumed["artifact"] != "report" or artifact_resumed["disk"] != "rootfs":
    raise SystemExit(artifact_resumed)
with open(os.path.join(state_dir, "artifacts", "resumed", "report.json"), "r", encoding="utf-8") as f:
    if json.load(f) != {"ok": True, "phase": "resumed"}:
        raise SystemExit("resumed artifact mismatch")
if quarantine["event"]["state"] != "quarantined" or quarantined["event"]["state"] != "quarantined":
    raise SystemExit(quarantined)
if quarantined["readiness"]["guestReady"]["ready"] is not True:
    raise SystemExit(quarantined)
with open(os.path.join(state_dir, "substrate", "substrate-smoke", "events.json"), "r", encoding="utf-8") as f:
    states = [event["state"] for event in json.load(f)]
for expected in ("prepared", "running", "halted", "quarantined"):
    if expected not in states:
        raise SystemExit(states)
if states.count("running") < 2:
    raise SystemExit(states)
PY

"$CLI" delete substrate-smoke --state-dir "$STATE_DIR/substrate" >"$STATE_DIR/substrate-delete.json"

python3 - "$STATE_DIR/substrate-delete.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    delete = json.load(f)
if delete["event"]["state"] != "stopped":
    raise SystemExit(delete)
PY

echo "firecracker workspace smoke passed"
