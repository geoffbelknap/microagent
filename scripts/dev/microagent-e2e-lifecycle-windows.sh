#!/usr/bin/env bash
set -euo pipefail

# windows-hyperv arm of the lifecycle-deep scenario: the backend-neutral
# lifecycle feature contract over a real VHD boot on Hyper-V. Covers
# create (+dry-run and validation failures), start, channel-true readiness,
# status/inspect/ps, connect --send, structured exec, logs, events history,
# stats sampling, halt + restart, clone of a stopped workspace booted and
# exec'd, quarantine semantics, artifacts list, images list, prune, and
# delete cleanup.
#
# cp and artifact extraction ride the guest exec channel (Open Decision #1
# resolved: guest-mediated copy with a transient maintenance boot for
# stopped workspaces). mke2fs-based segments (standalone rootfs build,
# images pull) stay on the ext4 lanes; mke2fs is never required on Windows.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

e2e_is_windows || e2e_skip "windows-hyperv lifecycle E2E requires a Windows host"
e2e_have_hcs || e2e_skip "Hyper-V HCS services (vmms/vmcompute) are not running"
for required in go python3; do
  e2e_require_cmd "$required" "$required is required for windows-hyperv lifecycle E2E"
done

STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-lifecycle-whv.XXXXXX")"
CLI="$STATE_DIR/microagent.exe"
WORKSPACE="lifecycle-whv"
CLONE="lifecycle-whv-clone"
KEEP_VAR="${MICROAGENT_KEEP_MICROAGENT_E2E_LIFECYCLE:-0}"
IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f}"
KERNEL="$HOME/.microagent/kernels/windows-hyperv/amd64/Image"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    "$CLI" kill "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" kill "$CLONE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" delete "$CLONE" --yes --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "$KEEP_VAR" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent windows lifecycle E2E state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
export GOFLAGS="${GOFLAGS:-} -modcacherw"

json_get() {
  python3 - "$1" "$2" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    value = json.load(f)
for part in sys.argv[2].split("."):
    if not part:
        continue
    if isinstance(value, list):
        value = value[int(part)]
    else:
        value = value[part]
print(value)
PY
}

expect_failure() {
  name="$1"
  expected="$2"
  shift 2
  if "$@" >"$STATE_DIR/$name.out" 2>"$STATE_DIR/$name.err"; then
    echo "$name unexpectedly succeeded" >&2
    exit 1
  fi
  if ! grep -Eiq -- "$expected" "$STATE_DIR/$name.out" "$STATE_DIR/$name.err"; then
    echo "$name failed without expected message: $expected" >&2
    cat "$STATE_DIR/$name.out" "$STATE_DIR/$name.err" >&2
    exit 1
  fi
}

# wait_for_ready <workspace> <output>: poll status until the workspace is
# running AND the exec and shell channels answer their hv_sock probes.
# Exec immediately after "running" races the guest boot, so readiness is
# the channel truth, not the HCS start (45s window like the Go smokes).
wait_for_ready() {
  workspace="$1"
  output="$2"
  deadline="$((SECONDS + 45))"
  while true; do
    "$CLI" status "$workspace" --state-dir "$STATE_DIR" >"$output"
    if python3 - "$output" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    status = json.load(handle)
event = status.get("event") or {}
readiness = status.get("readiness") or {}
if (
    event.get("state") == "running"
    and readiness.get("execReady", {}).get("ready")
    and readiness.get("shellReady", {}).get("ready")
):
    raise SystemExit(0)
raise SystemExit(1)
PY
    then
      return 0
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "workspace $workspace did not become exec/shell ready" >&2
      cat "$output" >&2
      return 1
    fi
    sleep 1
  done
}

e2e_step "build CLI and guest init"
( cd "$ROOT"; go build -buildvcs=false -o "$CLI" ./cmd/microagent )
GUEST_INIT="$STATE_DIR/microagent-guestinit"
( cd "$ROOT"; GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit )

e2e_step "kernel artifact"
if [ ! -r "$KERNEL" ]; then
  "$CLI" kernel install || e2e_skip "windows-hyperv kernel install failed"
fi
test -r "$KERNEL"

e2e_step "create dry-run and validation failures"
"$CLI" create "$WORKSPACE" --dry-run --image "$IMAGE" --network isolated --state-dir "$STATE_DIR" >"$STATE_DIR/dry-run.json"
test "$(json_get "$STATE_DIR/dry-run.json" workspace)" = "$WORKSPACE"
expect_failure invalid-name "invalid workspace name" \
  "$CLI" create "bad..\\name" --dry-run --image "$IMAGE" --state-dir "$STATE_DIR"
expect_failure invalid-restart "restart policy" \
  "$CLI" create invalid-restart --dry-run --image "$IMAGE" --restart sometimes --state-dir "$STATE_DIR"
expect_failure invalid-network "network.mode" \
  "$CLI" create invalid-network --dry-run --image "$IMAGE" --network made-up --state-dir "$STATE_DIR"
expect_failure status-missing "not found" \
  "$CLI" status no-such-workspace --state-dir "$STATE_DIR"

e2e_step "create a VHD workspace with setup, env, and a declared output"
# Local hosts may be non-elevated (HNS NAT fails); isolated keeps the
# scenario independent of host network privileges. 512 MiB: 256 overflows
# the busybox VHD build. MSYS2_ARG_CONV_EXCL keeps Git Bash from rewriting
# the guest path in the --output spec while --state-dir still auto-converts.
MSYS2_ARG_CONV_EXCL="report=" \
"$CLI" create "$WORKSPACE" \
  --image "$IMAGE" \
  --network isolated \
  --size-mib 512 \
  --service-command "sleep 600" \
  --setup "printf setup-ok > /setup.txt" \
  --env "LIFECYCLE_ENV=env-ok" \
  --output "report=/report.json" \
  --state-dir "$STATE_DIR" >"$STATE_DIR/create.json"
test "$(json_get "$STATE_DIR/create.json" workspace)" = "$WORKSPACE"
test "$(json_get "$STATE_DIR/create.json" network.mode)" = "isolated"
# The setup boot writes /setup.txt into the rootfs; a zero exit proves the
# guest root filesystem is genuinely writable.
test "$(json_get "$STATE_DIR/create.json" result.exit_code)" = "0"
test -e "$STATE_DIR/workspaces/$WORKSPACE/rootfs.vhd"

e2e_step "status / inspect / ps on the prepared workspace"
# create with setup commands boots the guest once to run them, so the
# workspace lands on stopped (prepared when no setup boot was needed).
"$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-prepared.json"
created_state="$(json_get "$STATE_DIR/status-prepared.json" event.state)"
case "$created_state" in
  prepared|stopped) ;;
  *) e2e_fail "post-create state = $created_state, want prepared or stopped" ;;
esac
"$CLI" inspect "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/inspect-prepared.json"
test "$(json_get "$STATE_DIR/inspect-prepared.json" event.identity.backend)" = "windows-hyperv"
"$CLI" ps --state-dir "$STATE_DIR" >"$STATE_DIR/ps-prepared.json"
grep -q "$WORKSPACE" "$STATE_DIR/ps-prepared.json"

e2e_step "start and wait for channel-true readiness"
"$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/start.json"
wait_for_ready "$WORKSPACE" "$STATE_DIR/status-running.json"
test "$(json_get "$STATE_DIR/status-running.json" readiness.execReady.ready)" = "True"
test "$(json_get "$STATE_DIR/status-running.json" readiness.shellReady.ready)" = "True"
"$CLI" ps --state-dir "$STATE_DIR" >"$STATE_DIR/ps-running.json"
grep -q "running" "$STATE_DIR/ps-running.json"
expect_failure start-running "already running" \
  "$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR"

e2e_step "connect --send round trip sees setup and env"
"$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "cat /setup.txt; printf env=%s \"\$LIFECYCLE_ENV\"" \
  --ready-timeout 45 \
  --timeout 10 >"$STATE_DIR/connect-running.txt"
grep -q "setup-ok" "$STATE_DIR/connect-running.txt"
grep -q "env=env-ok" "$STATE_DIR/connect-running.txt"

e2e_step "exec writes state the clone must inherit"
"$CLI" exec "$WORKSPACE" --state-dir "$STATE_DIR" -- \
  sh -c "printf persisted > /persist.txt; printf '{\"ok\":true}' > /report.json; sync" >"$STATE_DIR/exec-write.out"
"$CLI" exec "$WORKSPACE" --state-dir "$STATE_DIR" -- sh -c "cat /persist.txt" >"$STATE_DIR/exec-read.out"
grep -q "persisted" "$STATE_DIR/exec-read.out"

e2e_step "logs carry guest init output"
"$CLI" logs "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/logs-running.txt"
grep -q "microagent-init: starting" "$STATE_DIR/logs-running.txt"

e2e_step "artifacts list shows the declared output"
"$CLI" artifacts "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/artifacts-running.json"
grep -q '"report"' "$STATE_DIR/artifacts-running.json"

e2e_step "events history is the backend-neutral JSON array"
"$CLI" --json events "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/events-running.json"
python3 - "$STATE_DIR/events-running.json" "$STATE_DIR/$WORKSPACE/events.json" "$WORKSPACE" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    response = json.load(handle)
if response.get("workspace") != sys.argv[3]:
    raise SystemExit(f"events workspace = {response.get('workspace')!r}")
events = response.get("events")
if not isinstance(events, list) or not events:
    raise SystemExit(f"events is not a non-empty array: {events!r}")
states = [event.get("state") for event in events]
if "running" not in states:
    raise SystemExit(f"no running event in history: {states}")
if states.index("running") == 0:
    raise SystemExit(f"running cannot be the first lifecycle state: {states}")
with open(sys.argv[2], "r", encoding="utf-8") as handle:
    on_disk = json.load(handle)
if not isinstance(on_disk, list):
    raise SystemExit("events.json on disk is not a JSON array")
PY

e2e_step "stats sample HCS properties on a busy guest"
"$CLI" exec "$WORKSPACE" --state-dir "$STATE_DIR" -- \
  sh -c "end=\$(( \$(date +%s) + 8 )); while [ \$(date +%s) -lt \$end ]; do :; done" >"$STATE_DIR/exec-busy.out" &
BUSY_PID=$!
sleep 2
"$CLI" --json stats "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/stats-busy.json"
wait "$BUSY_PID"
python3 - "$STATE_DIR/stats-busy.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    stats = json.load(handle)
if stats.get("memoryBytes", 0) <= 0:
    raise SystemExit(f"memoryBytes not positive: {stats}")
cpu = stats.get("cpuPercent", -1)
# One busy shell loop should register clearly; cap at vCPUs * 100 plus
# sampling slack so a wild reading still fails.
if not (5 <= cpu <= 400):
    raise SystemExit(f"cpuPercent out of sane busy-guest range: {stats}")
PY

e2e_step "halt is clean and restart comes back ready"
"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt.json"
test "$(json_get "$STATE_DIR/halt.json" event.state)" = "halted"
"$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-halted.json"
test "$(json_get "$STATE_DIR/status-halted.json" event.state)" = "halted"
"$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/restart.json"
wait_for_ready "$WORKSPACE" "$STATE_DIR/status-restarted.json"
"$CLI" exec "$WORKSPACE" --state-dir "$STATE_DIR" -- sh -c "cat /persist.txt" >"$STATE_DIR/exec-after-restart.out"
grep -q "persisted" "$STATE_DIR/exec-after-restart.out"

e2e_step "guest-mediated cp and artifact extraction on the stopped workspace"
# Open Decision #1 resolved: cp/artifacts get ride the guest exec channel
# via a transient maintenance boot. Local endpoints use Windows-form paths
# (drive-absolute paths disambiguate from workspace endpoints).
"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt-for-copy.json"
mkdir -p "$STATE_DIR/cp-out"
"$CLI" cp "$WORKSPACE:/persist.txt" "$(e2e_host_path "$STATE_DIR/cp-out")" --state-dir "$STATE_DIR" >"$STATE_DIR/cp-from.json"
grep -q "persisted" "$STATE_DIR/cp-out/persist.txt"
printf "host-injected" >"$STATE_DIR/host-copy.txt"
"$CLI" cp "$(e2e_host_path "$STATE_DIR/host-copy.txt")" "$WORKSPACE:/host-copy.txt" --state-dir "$STATE_DIR" >"$STATE_DIR/cp-to.json"
test "$(json_get "$STATE_DIR/cp-to.json" direction)" = "to-workspace"
mkdir -p "$STATE_DIR/artifacts-out"
"$CLI" artifacts get "$WORKSPACE" report "$(e2e_host_path "$STATE_DIR/artifacts-out")" --state-dir "$STATE_DIR" >"$STATE_DIR/artifact-get.json"
grep -q '"ok":true' "$STATE_DIR/artifacts-out/report.json"
# The maintenance boots must leave the workspace stopped.
"$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-after-copy.json"
test "$(json_get "$STATE_DIR/status-after-copy.json" event.state)" = "halted"

e2e_step "clone a stopped workspace, boot it, and exec the clone"
"$CLI" clone "$WORKSPACE" "$CLONE" --state-dir "$STATE_DIR" >"$STATE_DIR/clone.json"
test "$(json_get "$STATE_DIR/clone.json" workspace)" = "$CLONE"
test "$(json_get "$STATE_DIR/clone.json" response.event.state)" = "prepared"
test -e "$STATE_DIR/workspaces/$CLONE/rootfs.vhd"
"$CLI" ps --state-dir "$STATE_DIR" >"$STATE_DIR/ps-cloned.json"
grep -q "$CLONE" "$STATE_DIR/ps-cloned.json"
"$CLI" start "$CLONE" --state-dir "$STATE_DIR" >"$STATE_DIR/clone-start.json"
wait_for_ready "$CLONE" "$STATE_DIR/clone-status-running.json"
"$CLI" exec "$CLONE" --state-dir "$STATE_DIR" -- sh -c "cat /persist.txt; cat /host-copy.txt" >"$STATE_DIR/clone-exec.out"
grep -q "persisted" "$STATE_DIR/clone-exec.out"
grep -q "host-injected" "$STATE_DIR/clone-exec.out"
"$CLI" stop "$CLONE" --state-dir "$STATE_DIR" >"$STATE_DIR/clone-stop.json"
test "$(json_get "$STATE_DIR/clone-stop.json" event.state)" = "stopped"

e2e_step "quarantine refuses connect and start, halt still works"
"$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/resume.json"
wait_for_ready "$WORKSPACE" "$STATE_DIR/status-resumed.json"
"$CLI" quarantine "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/quarantine.json"
test "$(json_get "$STATE_DIR/quarantine.json" event.state)" = "quarantined"
expect_failure connect-quarantined "quarantined" \
  "$CLI" connect "$WORKSPACE" --state-dir "$STATE_DIR" --send "echo no"
expect_failure start-quarantined "quarantined" \
  "$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR"
"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt-quarantined.json"
test "$(json_get "$STATE_DIR/halt-quarantined.json" event.state)" = "halted"
# kill on an already-stopped workspace is idempotent (the compute system is
# gone; the supervisor reconciles instead of failing).
"$CLI" kill "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/kill-halted.json"
test "$(json_get "$STATE_DIR/kill-halted.json" event.state)" = "stopped"

e2e_step "event history recorded the full lifecycle"
python3 - "$STATE_DIR/$WORKSPACE/events.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    states = [event["state"] for event in json.load(handle)]
for expected in ("running", "halted", "quarantined"):
    if expected not in states:
        raise SystemExit(f"missing {expected} in event history: {states}")
if states.count("running") < 3:
    raise SystemExit(f"expected at least three boots in event history: {states}")
PY

e2e_step "images and prune surfaces"
"$CLI" images list --state-dir "$STATE_DIR" >"$STATE_DIR/images.json"
json_get "$STATE_DIR/images.json" images >/dev/null
"$CLI" prune --state-dir "$STATE_DIR" >"$STATE_DIR/prune.json"

e2e_step "delete cleans workspace state"
"$CLI" delete "$CLONE" --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete-clone.json"
test ! -e "$STATE_DIR/workspaces/$CLONE/rootfs.vhd"
"$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete-workspace.json"
test ! -e "$STATE_DIR/workspaces/$WORKSPACE/rootfs.vhd"

echo "microagent windows-hyperv lifecycle E2E passed"
