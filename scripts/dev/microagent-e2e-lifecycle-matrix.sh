#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-lifecycle-matrix.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
WORKSPACE="feature-matrix"
CLONE="feature-clone"
ARTIFACT_DIR="$STATE_DIR/artifacts"
IMAGE="${MICROAGENT_NATS_IMAGE:-docker.io/library/nats@sha256:6e0cca2c6da79f0a3542ec5a3319dd10b1b05f5d8e8949afa8e9cdf6314bbf6c}"
EXPECTED_KERNEL_SHA="4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    "$CLI" stop "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" stop "$CLONE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" delete "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" delete "$CLONE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_LIFECYCLE_MATRIX:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E lifecycle matrix state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    echo "microagent E2E lifecycle matrix requires Linux amd64" >&2
    exit 2
    ;;
esac

for required in getcap debugfs; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "$required is required for microagent E2E lifecycle matrix" >&2
    exit 2
  fi
done

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

wait_for_status_ready() {
  workspace="$1"
  output="$2"
  deadline="$((SECONDS + 45))"
  while true; do
    "$CLI" status "$workspace" --state-dir "$STATE_DIR" >"$output"
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
  go build -buildvcs=false -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

if caps="$(getcap "$SUPERVISOR" 2>/dev/null)" && [ -n "$caps" ]; then
  echo "temporary supervisor unexpectedly has file capabilities: $caps" >&2
  exit 1
fi

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
"$CLI" profiles >"$STATE_DIR/profiles.json"

expect_failure invalid-restart "restart policy" \
  "$CLI" create invalid-restart --image "$IMAGE" --restart sometimes --state-dir "$STATE_DIR"
expect_failure invalid-network "network.mode" \
  "$CLI" create invalid-network --image "$IMAGE" --network made-up --state-dir "$STATE_DIR"
expect_failure invalid-publish "publish" \
  "$CLI" create invalid-publish --image "$IMAGE" --publish bad-mapping --state-dir "$STATE_DIR"
expect_failure reserved-disk "rootfs is reserved" \
  "$CLI" create invalid-disk --image "$IMAGE" --disk rootfs=/tmp/nope:/data:rw --state-dir "$STATE_DIR"
expect_failure mutable-rootfs "mutable" \
  "$CLI" rootfs build --image docker.io/library/nats:2.10.26-alpine --out "$STATE_DIR/mutable.ext4" --state-dir "$STATE_DIR/mutable-rootfs"

"$CLI" images pull "$IMAGE" \
  --state-dir "$STATE_DIR" \
  --arch amd64 \
  --guest-init "$GUEST_INIT" \
  --size-mib 192 >"$STATE_DIR/images-pull.json"
"$CLI" images tag "$IMAGE" local/nats-feature:probe --state-dir "$STATE_DIR" >"$STATE_DIR/images-tag.json"
"$CLI" images list --state-dir "$STATE_DIR" >"$STATE_DIR/images-list.json"

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
  sizeMiB: 192
network:
  mode: isolated
outputs:
  - name: report
    path: /matrix/report.json
YAML

(
  cd "$STATE_DIR/spec"
  "$CLI" create --file microagent.yaml \
    --state-dir "$STATE_DIR" \
    --kernel "$kernel_path" \
    --guest-init "$GUEST_INIT" >"$STATE_DIR/create-spec.json"
)

"$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-prepared.json"
"$CLI" ps --state-dir "$STATE_DIR" >"$STATE_DIR/ps-prepared.json"
"$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/start.json"
wait_for_status_ready "$WORKSPACE" "$STATE_DIR/status-running.json"
(
  printf 'echo INTERACTIVE_OK\n'
  sleep 1
  printf '\035'
) | "$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --ready-timeout 30 >"$STATE_DIR/connect-interactive.txt"
"$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "cat /seed.txt; cat /matrix/setup.txt; printf env=%s \"\$MATRIX_ENV\"; printf persisted > /matrix/persist.txt; printf '{\"ok\":true,\"phase\":\"running\"}' > /matrix/report.json; sync" \
  --ready-timeout 30 \
  --timeout 10 >"$STATE_DIR/connect-running.txt"
"$CLI" artifacts "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/artifacts-running.json"
"$CLI" logs "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/logs-running.txt"
"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt.json"
"$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-halted.json"

mkdir -p "$ARTIFACT_DIR/running" "$STATE_DIR/cp-out"
expect_failure unknown-artifact "not declared" \
  "$CLI" artifacts get "$WORKSPACE" no-such "$STATE_DIR/no-artifact" --state-dir "$STATE_DIR"
"$CLI" artifacts get "$WORKSPACE" report "$ARTIFACT_DIR/running" --state-dir "$STATE_DIR" >"$STATE_DIR/artifact-running.json"
printf "host-copied\n" >"$STATE_DIR/host-copy.txt"
"$CLI" cp "$STATE_DIR/host-copy.txt" "$WORKSPACE:/matrix/host-copy.txt" --state-dir "$STATE_DIR" >"$STATE_DIR/cp-to-workspace.json"
"$CLI" cp "$WORKSPACE:/matrix/persist.txt" "$STATE_DIR/cp-out" --state-dir "$STATE_DIR" >"$STATE_DIR/cp-from-workspace.json"
"$CLI" clone "$WORKSPACE" "$CLONE" --state-dir "$STATE_DIR" >"$STATE_DIR/clone.json"
"$CLI" ps --state-dir "$STATE_DIR" >"$STATE_DIR/ps-cloned.json"

"$CLI" start "$CLONE" --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/clone-start.json"
wait_for_status_ready "$CLONE" "$STATE_DIR/clone-status-running.json"
"$CLI" connect "$CLONE" \
  --state-dir "$STATE_DIR" \
  --send "cat /matrix/persist.txt; cat /matrix/host-copy.txt; printf '{\"ok\":true,\"phase\":\"clone\"}' > /matrix/report.json; sync" \
  --ready-timeout 30 \
  --timeout 10 >"$STATE_DIR/clone-connect.txt"
"$CLI" halt "$CLONE" --state-dir "$STATE_DIR" >"$STATE_DIR/clone-halt.json"
mkdir -p "$ARTIFACT_DIR/clone"
"$CLI" artifacts get "$CLONE" report "$ARTIFACT_DIR/clone" --state-dir "$STATE_DIR" >"$STATE_DIR/clone-artifact.json"

expect_failure connect-halted "console input is unavailable" \
  "$CLI" connect "$WORKSPACE" --state-dir "$STATE_DIR" --send "echo should-not-run"

"$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/resume.json"
wait_for_status_ready "$WORKSPACE" "$STATE_DIR/status-resumed.json"
expect_failure start-running "already running" \
  "$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$kernel_path"
"$CLI" quarantine "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/quarantine.json"
expect_failure connect-quarantined "quarantined" \
  "$CLI" connect "$WORKSPACE" --state-dir "$STATE_DIR" --send "echo no"
expect_failure start-quarantined "quarantined" \
  "$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$kernel_path"
"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt-quarantined.json"

"$CLI" delete "$CLONE" --state-dir "$STATE_DIR" >"$STATE_DIR/delete-clone.json"
"$CLI" delete "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/delete-workspace.json"
"$CLI" images rm local/nats-feature:probe --state-dir "$STATE_DIR" >"$STATE_DIR/images-rm-tag.json"
"$CLI" images tag "$IMAGE" local/nats-feature:delete-probe --state-dir "$STATE_DIR" >"$STATE_DIR/images-tag-delete.json"
"$CLI" images rm local/nats-feature:delete-probe --delete --state-dir "$STATE_DIR" >"$STATE_DIR/images-rm-delete.json"
"$CLI" images prune --state-dir "$STATE_DIR" >"$STATE_DIR/images-prune.json"
"$CLI" images prune --delete --state-dir "$STATE_DIR" >"$STATE_DIR/images-prune-delete.json"

python3 - "$STATE_DIR" "$WORKSPACE" "$CLONE" <<'PY'
import json
import os
import sys

state_dir, workspace, clone = sys.argv[1:4]

def read_json(name):
    with open(os.path.join(state_dir, name), "r", encoding="utf-8") as f:
        return json.load(f)

def read_text(name):
    with open(os.path.join(state_dir, name), "r", encoding="utf-8", errors="replace") as f:
        return f.read()

doctor = read_json("doctor.json")
profiles = read_json("profiles.json")
pull = read_json("images-pull.json")
tag = read_json("images-tag.json")
images = read_json("images-list.json")
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
quarantine = read_json("quarantine.json")
halt_quarantined = read_json("halt-quarantined.json")
delete_clone = read_json("delete-clone.json")
delete_workspace = read_json("delete-workspace.json")
rm_delete = read_json("images-rm-delete.json")
prune_delete = read_json("images-prune-delete.json")

if doctor.get("ok") is not True or doctor.get("backend") != "firecracker":
    raise SystemExit(doctor)
if not any(profile.get("name") == "tiny" for profile in profiles.get("profiles", [])):
    raise SystemExit(profiles)
if (pull.get("imageRef") or pull.get("image_ref", "")) == "" or (pull.get("outputPath") or pull.get("output_path", "")) == "":
    raise SystemExit(pull)
if (tag.get("imageRef") or tag.get("image_ref")) != "local/nats-feature:probe":
    raise SystemExit(tag)
if not any((image.get("imageRef") or image.get("image_ref")) == "local/nats-feature:probe" for image in images.get("images", [])):
    raise SystemExit(images)
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
if running.get("verification", {}).get("ok") is not False:
    raise SystemExit(running)
if not any(item.get("artifact") == "rootfs" for item in running.get("verification", {}).get("divergence", [])):
    raise SystemExit(running)
connect = read_text("connect-running.txt")
for needle in ("seed-from-spec", "setup-ok", "env=env-ok"):
    if needle not in connect:
        raise SystemExit(connect)
if "INTERACTIVE_OK" not in read_text("connect-interactive.txt"):
    raise SystemExit(read_text("connect-interactive.txt"))
if "microagent-init: starting" not in read_text("logs-running.txt"):
    raise SystemExit("logs missing guest init output")
if halted.get("event", {}).get("state") != "halted":
    raise SystemExit(halted)
if artifact.get("artifact") != "report" or artifact.get("disk") != "rootfs":
    raise SystemExit(artifact)
with open(os.path.join(state_dir, "artifacts", "running", "report.json"), "r", encoding="utf-8") as f:
    if json.load(f) != {"ok": True, "phase": "running"}:
        raise SystemExit("running artifact mismatch")
if copy_to.get("direction") != "to-workspace" or copy_from.get("direction") != "from-workspace":
    raise SystemExit((copy_to, copy_from))
with open(os.path.join(state_dir, "cp-out", "persist.txt"), "r", encoding="utf-8") as f:
    if f.read() != "persisted":
        raise SystemExit("copied persisted file mismatch")
if clone_result.get("workspace") != clone or clone_result.get("response", {}).get("event", {}).get("state") != "prepared":
    raise SystemExit(clone_result)
if clone_running.get("event", {}).get("state") != "running":
    raise SystemExit(clone_running)
clone_output = read_text("clone-connect.txt")
for needle in ("persisted", "host-copied"):
    if needle not in clone_output:
        raise SystemExit(clone_output)
with open(os.path.join(state_dir, "artifacts", "clone", "report.json"), "r", encoding="utf-8") as f:
    if json.load(f) != {"ok": True, "phase": "clone"}:
        raise SystemExit("clone artifact mismatch")
if clone_artifact.get("artifact") != "report":
    raise SystemExit(clone_artifact)
if quarantine.get("event", {}).get("state") != "quarantined":
    raise SystemExit(quarantine)
if halt_quarantined.get("event", {}).get("state") != "halted":
    raise SystemExit(halt_quarantined)
if delete_clone.get("event", {}).get("state") != "stopped" or delete_workspace.get("event", {}).get("state") != "stopped":
    raise SystemExit((delete_clone, delete_workspace))
if "removed" not in rm_delete or "removed" not in prune_delete:
    raise SystemExit((rm_delete, prune_delete))
PY

echo "microagent E2E lifecycle matrix passed"
