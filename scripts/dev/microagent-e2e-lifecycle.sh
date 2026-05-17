#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

default_backend() {
  case "$(uname -s):$(uname -m)" in
    Linux:x86_64|Linux:amd64)
      printf '%s\n' firecracker
      ;;
    Darwin:arm64)
      printf '%s\n' applevf
      ;;
    *)
      printf '%s\n' unsupported
      ;;
  esac
}

BACKEND="${MICROAGENT_E2E_BACKEND:-$(default_backend)}"

if [ "$BACKEND" = "firecracker" ]; then
  exec "$ROOT/scripts/dev/microagent-e2e-lifecycle-matrix.sh"
fi

if [ "$BACKEND" != "applevf" ]; then
  echo "microagent lifecycle E2E does not support backend lane: $BACKEND" >&2
  exit 2
fi

case "$(uname -s):$(uname -m)" in
  Darwin:arm64)
    ;;
  *)
    echo "Apple VF lifecycle E2E requires macOS on Apple silicon" >&2
    exit 2
    ;;
esac

SUPERVISOR="${MICROAGENT_APPLEVF_SUPERVISOR:-$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor}"
KERNEL="${MICROAGENT_APPLEVF_KERNEL:-$HOME/.microagent/kernels/apple-vf/arm64/Image}"
if [ ! -r "$KERNEL" ] && [ -r "$HOME/.microagent/kernels/apple-vf/Image" ]; then
  KERNEL="$HOME/.microagent/kernels/apple-vf/Image"
fi
IMAGE="${MICROAGENT_APPLEVF_BOOT_IMAGE:-docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6}"
ARCH="${MICROAGENT_APPLEVF_BOOT_ARCH:-arm64}"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-lifecycle-applevf.XXXXXX")"
WORKSPACE="lifecycle-e2e"
CLONE="lifecycle-clone"
FORCE_DELETE_WORKSPACE="lifecycle-force-delete"
CLI="$STATE_DIR/microagent"
GUEST_INIT="$STATE_DIR/microagent-guestinit"
ARTIFACT_DIR="$STATE_DIR/artifacts"

cleanup() {
  status="$?"
  if [ -x "$CLI" ] && [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_LIFECYCLE:-0}" != "1" ]; then
    "$CLI" stop "$WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" stop "$CLONE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" kill "$FORCE_DELETE_WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" delete "$CLONE" --yes --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" delete "$FORCE_DELETE_WORKSPACE" --force --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_LIFECYCLE:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E lifecycle Apple VF state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

if [ ! -r "$KERNEL" ]; then
  echo "kernel is not readable at $KERNEL" >&2
  exit 2
fi
if [ ! -x "$SUPERVISOR" ]; then
  echo "supervisor is not executable at $SUPERVISOR; run scripts/dev/applevf-supervisor-build.sh" >&2
  exit 2
fi
export MICROAGENT_APPLEVF_SUPERVISOR="$SUPERVISOR"

if command -v mke2fs >/dev/null 2>&1; then
  MKE2FS="$(command -v mke2fs)"
elif [ -x /opt/homebrew/opt/e2fsprogs/sbin/mke2fs ]; then
  MKE2FS="/opt/homebrew/opt/e2fsprogs/sbin/mke2fs"
else
  echo "mke2fs not found; install e2fsprogs" >&2
  exit 2
fi

if command -v debugfs >/dev/null 2>&1; then
  DEBUGFS="$(command -v debugfs)"
elif [ -x /opt/homebrew/opt/e2fsprogs/sbin/debugfs ]; then
  DEBUGFS="/opt/homebrew/opt/e2fsprogs/sbin/debugfs"
else
  echo "debugfs not found; install e2fsprogs" >&2
  exit 2
fi

wait_for_status_ready() {
  local workspace="$1"
  local output="$2"
  local deadline="$((SECONDS + 45))"
  while true; do
    "$CLI" status "$workspace" --state-dir "$STATE_DIR" >"$output"
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
      echo "workspace $workspace did not become ready" >&2
      cat "$output" >&2
      return 1
    fi
    sleep 1
  done
}

expect_failure() {
  name="$1"
  expected="$2"
  shift 2
  if "$@" >"$STATE_DIR/${name}.out" 2>"$STATE_DIR/${name}.err"; then
    echo "$name unexpectedly succeeded" >&2
    exit 1
  fi
  if ! grep -qi "$expected" "$STATE_DIR/${name}.err"; then
    echo "$name failed without expected message: $expected" >&2
    cat "$STATE_DIR/${name}.err" >&2
    exit 1
  fi
}

(
  cd "$ROOT"
  go build -buildvcs=false -o "$CLI" ./cmd/microagent
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

"$CLI" doctor --backend apple-vf --arch "$ARCH" --supervisor "$SUPERVISOR" >"$STATE_DIR/doctor.json"
"$CLI" profiles >"$STATE_DIR/profiles.json"

expect_failure invalid-restart "restart policy" \
  "$CLI" create invalid-restart --image "$IMAGE" --restart sometimes --state-dir "$STATE_DIR" --backend apple-vf --supervisor "$SUPERVISOR"
expect_failure invalid-network "network.mode" \
  "$CLI" create invalid-network --image "$IMAGE" --network made-up --state-dir "$STATE_DIR" --backend apple-vf --supervisor "$SUPERVISOR"
expect_failure invalid-publish "publish" \
  "$CLI" create invalid-publish --image "$IMAGE" --publish bad-mapping --state-dir "$STATE_DIR" --backend apple-vf --supervisor "$SUPERVISOR"
expect_failure reserved-disk "rootfs is reserved" \
  "$CLI" create invalid-disk --image "$IMAGE" --disk rootfs=/tmp/nope:/data:rw --state-dir "$STATE_DIR" --backend apple-vf --supervisor "$SUPERVISOR"

mkdir -p "$STATE_DIR/spec"
printf "seed-from-spec\n" >"$STATE_DIR/spec/seed.txt"
cat >"$STATE_DIR/spec/microagent.yaml" <<YAML
name: $WORKSPACE
image: $IMAGE
profile: tiny
restart: never
setup:
  - mkdir -p /matrix
  - printf setup-ok > /matrix/setup.txt
files:
  - src: ./seed.txt
    dst: /seed.txt
    mode: "0644"
env:
  MATRIX_ENV: env-ok
resources:
  memoryMiB: 512
  cpuCount: 2
  sizeMiB: 128
network:
  mode: isolated
outputs:
  - name: report
    path: /matrix/report.json
YAML

(
  cd "$STATE_DIR/spec"
  "$CLI" create --file microagent.yaml \
    --backend apple-vf \
    --state-dir "$STATE_DIR" \
    --mke2fs "$MKE2FS" \
    --kernel "$KERNEL" \
    --guest-init "$GUEST_INIT" \
    --supervisor "$SUPERVISOR" >"$STATE_DIR/create-spec.json"
)

"$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-prepared.json"
"$CLI" ps --state-dir "$STATE_DIR" >"$STATE_DIR/ps-prepared.json"
"$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$KERNEL" --supervisor "$SUPERVISOR" >"$STATE_DIR/start.json"
wait_for_status_ready "$WORKSPACE" "$STATE_DIR/status-running.json"
"$CLI" ps --state-dir "$STATE_DIR" >"$STATE_DIR/ps-running.json"
"$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "cat /seed.txt; cat /matrix/setup.txt; printf env=%s \"\$MATRIX_ENV\"; printf persisted > /matrix/persist.txt; printf '{\"ok\":true,\"phase\":\"running\"}' > /matrix/report.json; sync" \
  --ready-timeout 30 \
  --timeout 10 >"$STATE_DIR/connect-running.txt"
"$CLI" artifacts "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/artifacts-running.json"
"$CLI" logs "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/logs-running.txt"
"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/halt.json"
"$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-halted.json"

mkdir -p "$ARTIFACT_DIR/running" "$STATE_DIR/cp-out"
expect_failure unknown-artifact "not declared" \
  "$CLI" artifacts get "$WORKSPACE" no-such "$STATE_DIR/no-artifact" --state-dir "$STATE_DIR" --debugfs "$DEBUGFS"
"$CLI" artifacts get "$WORKSPACE" report "$ARTIFACT_DIR/running" \
  --state-dir "$STATE_DIR" \
  --debugfs "$DEBUGFS" >"$STATE_DIR/artifact-running.json"
printf "host-copied\n" >"$STATE_DIR/host-copy.txt"
"$CLI" cp "$STATE_DIR/host-copy.txt" "$WORKSPACE:/matrix/host-copy.txt" \
  --state-dir "$STATE_DIR" \
  --debugfs "$DEBUGFS" >"$STATE_DIR/cp-to-workspace.json"
"$CLI" cp "$WORKSPACE:/matrix/persist.txt" "$STATE_DIR/cp-out" \
  --state-dir "$STATE_DIR" \
  --debugfs "$DEBUGFS" >"$STATE_DIR/cp-from-workspace.json"
"$CLI" clone "$WORKSPACE" "$CLONE" --state-dir "$STATE_DIR" >"$STATE_DIR/clone.json"
"$CLI" ps --state-dir "$STATE_DIR" >"$STATE_DIR/ps-cloned.json"

"$CLI" start "$CLONE" --state-dir "$STATE_DIR" --kernel "$KERNEL" --supervisor "$SUPERVISOR" >"$STATE_DIR/clone-start.json"
wait_for_status_ready "$CLONE" "$STATE_DIR/clone-status-running.json"
"$CLI" connect "$CLONE" \
  --state-dir "$STATE_DIR" \
  --send "cat /matrix/persist.txt; cat /matrix/host-copy.txt; printf '{\"ok\":true,\"phase\":\"clone\"}' > /matrix/report.json; sync" \
  --ready-timeout 30 \
  --timeout 10 >"$STATE_DIR/clone-connect.txt"
"$CLI" halt "$CLONE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/clone-halt.json"
mkdir -p "$ARTIFACT_DIR/clone"
"$CLI" artifacts get "$CLONE" report "$ARTIFACT_DIR/clone" \
  --state-dir "$STATE_DIR" \
  --debugfs "$DEBUGFS" >"$STATE_DIR/clone-artifact.json"

"$CLI" clone "$WORKSPACE" "$FORCE_DELETE_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/force-delete-clone.json"
"$CLI" start "$FORCE_DELETE_WORKSPACE" --state-dir "$STATE_DIR" --kernel "$KERNEL" --supervisor "$SUPERVISOR" >"$STATE_DIR/force-delete-start.json"
wait_for_status_ready "$FORCE_DELETE_WORKSPACE" "$STATE_DIR/force-delete-status-running.json"
"$CLI" delete "$FORCE_DELETE_WORKSPACE" --force --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/force-delete-running.json"
test ! -e "$STATE_DIR/workspaces/$FORCE_DELETE_WORKSPACE"

expect_failure connect-halted "console input is unavailable" \
  "$CLI" connect "$WORKSPACE" --state-dir "$STATE_DIR" --send "echo should-not-run"

"$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$KERNEL" --supervisor "$SUPERVISOR" >"$STATE_DIR/resume.json"
wait_for_status_ready "$WORKSPACE" "$STATE_DIR/status-resumed.json"
expect_failure start-running "already running" \
  "$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$KERNEL" --supervisor "$SUPERVISOR"
"$CLI" quarantine "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/quarantine.json"
expect_failure connect-quarantined "quarantined" \
  "$CLI" connect "$WORKSPACE" --state-dir "$STATE_DIR" --send "echo no"
expect_failure start-quarantined "quarantined" \
  "$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$KERNEL" --supervisor "$SUPERVISOR"
"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/halt-quarantined.json"
cp "$STATE_DIR/$WORKSPACE/events.json" "$STATE_DIR/events.json"

"$CLI" delete "$CLONE" --yes --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/delete-clone.json"
"$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/delete-workspace.json"

python3 - "$STATE_DIR" "$WORKSPACE" "$CLONE" "$FORCE_DELETE_WORKSPACE" <<'PY'
import json
import os
import sys

state_dir, workspace, clone, force_delete = sys.argv[1:5]

def read_json(name):
    with open(os.path.join(state_dir, name), "r", encoding="utf-8") as handle:
        return json.load(handle)

def read_text(name):
    with open(os.path.join(state_dir, name), "r", encoding="utf-8", errors="replace") as handle:
        return handle.read()

doctor = read_json("doctor.json")
profiles = read_json("profiles.json")
create = read_json("create-spec.json")
prepared = read_json("status-prepared.json")
running = read_json("status-running.json")
halted = read_json("status-halted.json")
artifact = read_json("artifact-running.json")
copy_to = read_json("cp-to-workspace.json")
copy_from = read_json("cp-from-workspace.json")
clone_result = read_json("clone.json")
clone_running = read_json("clone-status-running.json")
clone_artifact = read_json("clone-artifact.json")
force_delete_clone = read_json("force-delete-clone.json")
force_delete_running = read_json("force-delete-status-running.json")
force_delete_result = read_json("force-delete-running.json")
resumed = read_json("status-resumed.json")
quarantine = read_json("quarantine.json")
halt_quarantined = read_json("halt-quarantined.json")
delete_clone = read_json("delete-clone.json")
delete_workspace = read_json("delete-workspace.json")

if doctor.get("ok") is not True or doctor.get("backend") != "apple-vf":
    raise SystemExit(doctor)
if not any(profile.get("name") == "tiny" for profile in profiles.get("profiles", [])):
    raise SystemExit(profiles)
if create.get("workspace") != workspace or create.get("response", {}).get("event", {}).get("state") not in ("prepared", "stopped"):
    raise SystemExit(create)
if create.get("profile") != "tiny" or create.get("restart") != "never":
    raise SystemExit(create)
if create.get("network", {}).get("mode") != "isolated":
    raise SystemExit(create)
if prepared.get("event", {}).get("state") not in ("prepared", "stopped"):
    raise SystemExit(prepared)
if running.get("event", {}).get("state") != "running":
    raise SystemExit(running)
verification = running.get("verification", {})
if verification.get("ok") is False:
    if not any(item.get("artifact") == "rootfs" for item in verification.get("divergence", [])):
        raise SystemExit(running)
elif verification.get("ok") is not True:
    raise SystemExit(running)
if not running.get("readiness", {}).get("guestReady", {}).get("ready"):
    raise SystemExit(running)
if not running.get("readiness", {}).get("shellReady", {}).get("ready"):
    raise SystemExit(running)
connect = read_text("connect-running.txt")
for needle in ("seed-from-spec", "setup-ok", "env=env-ok"):
    if needle not in connect:
        raise SystemExit(connect)
if "microagent-init: starting" not in read_text("logs-running.txt"):
    raise SystemExit("logs missing guest init output")
if halted.get("event", {}).get("state") != "halted":
    raise SystemExit(halted)
if artifact.get("artifact") != "report" or artifact.get("disk") != "rootfs":
    raise SystemExit(artifact)
with open(os.path.join(state_dir, "artifacts", "running", "report.json"), "r", encoding="utf-8") as handle:
    if json.load(handle) != {"ok": True, "phase": "running"}:
        raise SystemExit("running artifact mismatch")
if copy_to.get("direction") != "to-workspace" or copy_from.get("direction") != "from-workspace":
    raise SystemExit((copy_to, copy_from))
with open(os.path.join(state_dir, "cp-out", "persist.txt"), "r", encoding="utf-8") as handle:
    if handle.read() != "persisted":
        raise SystemExit("copied persisted file mismatch")
if clone_result.get("workspace") != clone or clone_result.get("response", {}).get("event", {}).get("state") != "prepared":
    raise SystemExit(clone_result)
if clone_running.get("event", {}).get("state") != "running":
    raise SystemExit(clone_running)
clone_output = read_text("clone-connect.txt")
for needle in ("persisted", "host-copied"):
    if needle not in clone_output:
        raise SystemExit(clone_output)
with open(os.path.join(state_dir, "artifacts", "clone", "report.json"), "r", encoding="utf-8") as handle:
    if json.load(handle) != {"ok": True, "phase": "clone"}:
        raise SystemExit("clone artifact mismatch")
if clone_artifact.get("artifact") != "report":
    raise SystemExit(clone_artifact)
if force_delete_clone.get("workspace") != force_delete or force_delete_clone.get("response", {}).get("event", {}).get("state") != "prepared":
    raise SystemExit(force_delete_clone)
if force_delete_running.get("event", {}).get("state") != "running":
    raise SystemExit(force_delete_running)
if force_delete_result.get("event", {}).get("state") != "stopped":
    raise SystemExit(force_delete_result)
if resumed.get("event", {}).get("state") != "running":
    raise SystemExit(resumed)
if quarantine.get("event", {}).get("state") != "quarantined":
    raise SystemExit(quarantine)
if halt_quarantined.get("event", {}).get("state") != "halted":
    raise SystemExit(halt_quarantined)
if delete_clone.get("event", {}).get("state") != "stopped" or delete_workspace.get("event", {}).get("state") != "stopped":
    raise SystemExit((delete_clone, delete_workspace))
with open(os.path.join(state_dir, "events.json"), "r", encoding="utf-8") as handle:
    states = [event["state"] for event in json.load(handle)]
for expected in ("running", "halted", "quarantined"):
    if expected not in states:
        raise SystemExit(states)
if states.count("running") < 2:
    raise SystemExit(states)
PY

echo "microagent E2E lifecycle passed for applevf"
