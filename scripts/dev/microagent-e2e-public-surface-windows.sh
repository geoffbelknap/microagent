#!/usr/bin/env bash
set -euo pipefail

# windows-hyperv arm of the public-surface scenario: the CLI surface checks
# that are well-defined on a Windows host plus a real VHD boot/run/result
# cycle over hv_sock. Segments of the POSIX scenario that lean on ext4
# tooling (standalone rootfs build, mke2fs-made disks, debugfs artifact
# extraction) stay deferred per the windows-hyperv plan until VHD volumes
# and guest-mediated copy land.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

e2e_is_windows || e2e_skip "windows-hyperv public surface E2E requires a Windows host"
e2e_have_hcs || e2e_skip "Hyper-V HCS services (vmms/vmcompute) are not running"
for required in go python3; do
  e2e_require_cmd "$required" "$required is required for windows-hyperv public surface E2E"
done

STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-public-surface-whv.XXXXXX")"
CLI="$STATE_DIR/microagent.exe"
WORKSPACE="public-surface-whv"
PERF_WORKSPACE="public-perf-whv"
KEEP_VAR="${MICROAGENT_KEEP_MICROAGENT_E2E_PUBLIC_SURFACE:-0}"
IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f}"
KERNEL="$HOME/.microagent/kernels/windows-hyperv/amd64/Image"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    for workspace in "$WORKSPACE" "$PERF_WORKSPACE"; do
      "$CLI" kill "$workspace" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      "$CLI" delete "$workspace" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    done
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "$KEEP_VAR" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent windows public surface E2E state at $STATE_DIR" >&2
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

e2e_step "build CLI and guest init"
( cd "$ROOT"; go build -buildvcs=false -o "$CLI" ./cmd/microagent )
GUEST_INIT="$STATE_DIR/microagent-guestinit"
( cd "$ROOT"; GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit )

e2e_step "kernel artifact"
if [ ! -r "$KERNEL" ]; then
  "$CLI" kernel install || e2e_skip "windows-hyperv kernel install failed"
fi
test -r "$KERNEL"

e2e_step "version / contract / profiles"
"$CLI" version >"$STATE_DIR/version.out"
grep -q "microagent" "$STATE_DIR/version.out"
"$CLI" --json contract >"$STATE_DIR/contract.json"
test "$(json_get "$STATE_DIR/contract.json" version)" = "agent-runtime.v1"
"$CLI" profiles >"$STATE_DIR/profiles.json"
grep -q '"tiny"' "$STATE_DIR/profiles.json"

e2e_step "host / doctor report the windows-hyperv backend"
"$CLI" --json host --backend windows-hyperv >"$STATE_DIR/host.json"
test "$(json_get "$STATE_DIR/host.json" host.backend)" = "windows-hyperv"
"$CLI" --json doctor --backend windows-hyperv >"$STATE_DIR/doctor.json"
test "$(json_get "$STATE_DIR/doctor.json" ok)" = "True"
test "$(json_get "$STATE_DIR/doctor.json" kernel.status)" = "present"

e2e_step "create dry-run and validation failures"
"$CLI" create "$WORKSPACE" --dry-run --image "$IMAGE" --network isolated --state-dir "$STATE_DIR" >"$STATE_DIR/dry-run.json"
test "$(json_get "$STATE_DIR/dry-run.json" workspace)" = "$WORKSPACE"
expect_failure run-missing-image "run requires --image" "$CLI" run --name missing-image --state-dir "$STATE_DIR"
expect_failure invalid-profile "unknown resource profile" "$CLI" create bad-profile --dry-run --image "$IMAGE" --profile definitely-not-a-profile --state-dir "$STATE_DIR"

e2e_step "run boots a VHD workspace and returns the structured result"
"$CLI" run -image "$IMAGE" -name "$WORKSPACE" -network isolated -keep -timeout 180 \
  -exec "echo PUBLIC_SURFACE_WHV_OK" --state-dir "$STATE_DIR" >"$STATE_DIR/run.json"
test "$(json_get "$STATE_DIR/run.json" response.result.exitCode)" = "0"
json_get "$STATE_DIR/run.json" response.result.stdout | grep -q "PUBLIC_SURFACE_WHV_OK"
test -e "$STATE_DIR/workspaces/$WORKSPACE/rootfs.vhd"

e2e_step "result / status / ps surfaces"
"$CLI" result "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/result.json"
json_get "$STATE_DIR/result.json" result.stdout | grep -q "PUBLIC_SURFACE_WHV_OK"
"$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status.json"
test "$(json_get "$STATE_DIR/status.json" event.state)" = "stopped"
test "$(json_get "$STATE_DIR/status.json" readiness.resultReady.ready)" = "True"
"$CLI" list --state-dir "$STATE_DIR" >"$STATE_DIR/ps.json"
grep -q "$WORKSPACE" "$STATE_DIR/ps.json"

e2e_step "images and prune surfaces"
"$CLI" image list --state-dir "$STATE_DIR" >"$STATE_DIR/images.json"
json_get "$STATE_DIR/images.json" images >/dev/null
"$CLI" image prune --state-dir "$STATE_DIR" >"$STATE_DIR/prune.json"

e2e_step "perf footprint and steady sample HCS statistics on a running workspace"
"$CLI" create "$PERF_WORKSPACE" --image "$IMAGE" --network isolated --size-mib 512 \
  --service-command "sleep 300" --guest-init "$GUEST_INIT" --state-dir "$STATE_DIR" >"$STATE_DIR/create-perf.json"
"$CLI" start "$PERF_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/start-perf.json"
perf_ready_deadline="$((SECONDS + 60))"
while true; do
  if "$CLI" --json status "$PERF_WORKSPACE" --state-dir "$STATE_DIR" 2>/dev/null \
    | python3 -c 'import json,sys; s=json.load(sys.stdin); r=s.get("readiness") or {}; sys.exit(0 if (s.get("event") or {}).get("state")=="running" and (r.get("execReady") or {}).get("ready") else 1)'; then
    break
  fi
  if [ "$SECONDS" -ge "$perf_ready_deadline" ]; then
    echo "perf workspace never became running+ready" >&2
    exit 1
  fi
  sleep 1
done
# windows-hyperv has no host guest PID; footprint/steady read HCS statistics.
"$CLI" --json perf footprint "$PERF_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/perf-footprint.json"
python3 - "$STATE_DIR/perf-footprint.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
if d.get("rss_kib", 0) <= 0 or d.get("state") != "running" or d.get("backend") != "windows-hyperv":
    raise SystemExit(f"footprint unexpected: {d}")
PY
"$CLI" --json perf steady "$PERF_WORKSPACE" --state-dir "$STATE_DIR" --duration 1 --interval 1 >"$STATE_DIR/perf-steady.json"
python3 - "$STATE_DIR/perf-steady.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
if d.get("summary", {}).get("count", 0) < 1 or d.get("summary", {}).get("min_kib", 0) <= 0:
    raise SystemExit(f"steady unexpected: {d}")
PY
"$CLI" kill "$PERF_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/kill-perf.json"
"$CLI" delete "$PERF_WORKSPACE" --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete-perf.json"

e2e_step "perf boot validation failures and isolated measured boots"
expect_failure perf-boot-zero-iterations "iterations must be positive" \
  "$CLI" --json perf boot --image "$IMAGE" --profile tiny --network isolated \
  --state-dir "$STATE_DIR/perf-zero-iterations" --exec "true" --iterations 0 --timeout 90
expect_failure perf-boot-zero-timeout "timeout must be positive" \
  "$CLI" --json perf boot --image "$IMAGE" --profile tiny --network isolated \
  --state-dir "$STATE_DIR/perf-zero-timeout" --exec "true" --iterations 1 --timeout 0
"$CLI" --json perf boot --image "$IMAGE" --profile definitely-not-a-profile --network isolated \
  --state-dir "$STATE_DIR/perf-invalid-profile" --exec "true" --iterations 1 --timeout 90 >"$STATE_DIR/perf-invalid-profile.json"
python3 - "$STATE_DIR/perf-invalid-profile.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
if d.get("summary", {}).get("count") != 1 or d.get("iterations", [])[0].get("ok") is not False:
    raise SystemExit(f"invalid-profile boot unexpected: {d}")
if "unknown resource profile" not in d.get("iterations", [])[0].get("error", ""):
    raise SystemExit(f"invalid-profile error unexpected: {d}")
PY
# Two real isolated boots: the tiny profile's 512 MiB rootfs covers the
# busybox VHD build; isolated needs no HNS elevation on this host or CI.
"$CLI" --json perf boot --image "$IMAGE" --profile tiny --network isolated \
  --state-dir "$STATE_DIR" --exec "printf PERF_BOOT_OK" --iterations 2 --timeout 240 >"$STATE_DIR/perf-boot.json"
python3 - "$STATE_DIR/perf-boot.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
summary = d.get("summary", {})
iterations = d.get("iterations", [])
if d.get("benchmark") != "boot" or summary.get("count") != 2 or len(iterations) != 2:
    raise SystemExit(f"perf boot shape unexpected: {d}")
if not all(item.get("ok") is True for item in iterations):
    raise SystemExit(f"perf boot iteration failed: {d}")
if not all(item.get("duration_ms", 0) > 0 for item in iterations):
    raise SystemExit(f"perf boot durations unexpected: {d}")
if summary.get("min_ms", 0) <= 0 or summary.get("avg_ms", 0) <= 0 or summary.get("max_ms", 0) < summary.get("min_ms", 0):
    raise SystemExit(f"perf boot summary unexpected: {d}")
PY

e2e_step "delete cleans workspace state"
"$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" >/dev/null
test ! -e "$STATE_DIR/workspaces/$WORKSPACE/rootfs.vhd"

echo "microagent windows-hyperv public surface E2E passed"
