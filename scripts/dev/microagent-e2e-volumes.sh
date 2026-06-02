#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

# Managed named volumes: registry lifecycle, ext4 backing, attach-by-name
# persistence across runs, and single-attach enforcement. The attach tests boot
# isolated microVMs (no network), so this needs a VM backend and mke2fs.
e2e_require_vm
e2e_require_cmd mke2fs "mke2fs (e2fsprogs) is required to format named volumes"

STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-volumes.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox:1.36}"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    "$CLI" kill holder --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" delete holder --force --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_VOLUMES:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E volumes state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

cd "$ROOT"
export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
e2e_build_firecracker_stack "$CLI" "$SUPERVISOR" "$GUEST_INIT"

"$CLI" kernel install --backend firecracker --arch amd64 >"$STATE_DIR/kernel-install.json" 2>/dev/null || e2e_fail "kernel install"
kernel_path="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["path"])' "$STATE_DIR/kernel-install.json")"
[ -r "$kernel_path" ] || e2e_fail "kernel install did not produce a readable image"

run_iso() { # run_iso <name-hint> <volume-spec> <exec>
  "$CLI" run --image "$IMAGE" --network isolated \
    --kernel "$kernel_path" --guest-init "$GUEST_INIT" --supervisor "$SUPERVISOR" \
    --state-dir "$STATE_DIR" --size-mib 128 --result-port 0 --timeout 40 --keep \
    --volume "$2" --exec "$3"
}

e2e_step "volume create / ls / inspect"
"$CLI" volume create data --size-mib 32 --state-dir "$STATE_DIR" >/dev/null || e2e_fail "volume create"
"$CLI" volume ls --state-dir "$STATE_DIR" | grep -q "data" || e2e_fail "volume ls missing data"
"$CLI" --json volume inspect data --state-dir "$STATE_DIR" | grep -q '"size_mib": 32' || e2e_fail "volume inspect size"
file "$STATE_DIR/volumes/data.ext4" | grep -qi "ext4 filesystem" || e2e_fail "backing file is not ext4"

e2e_step "duplicate and invalid names are rejected"
if "$CLI" volume create data --state-dir "$STATE_DIR" >/dev/null 2>&1; then e2e_fail "duplicate volume allowed"; fi
if "$CLI" volume create Bad_Name --state-dir "$STATE_DIR" >/dev/null 2>&1; then e2e_fail "invalid name allowed"; fi

e2e_step "attach-by-name persists data across separate runs"
run_iso writer "data:/work" "echo persisted-ok > /work/marker" >"$STATE_DIR/w1.json" 2>&1 || { cat "$STATE_DIR/w1.json"; e2e_fail "write run"; }
run_iso reader "data:/work" "cat /work/marker" >"$STATE_DIR/w2.json" 2>&1 || { cat "$STATE_DIR/w2.json"; e2e_fail "read run"; }
grep -q "persisted-ok" "$STATE_DIR/w2.json" || e2e_fail "volume did not persist data across runs"

e2e_step "single-attach: a running holder blocks a second attach"
"$CLI" create holder --image "$IMAGE" --network isolated --service-command "sleep 120" \
  --volume data:/work --kernel "$kernel_path" --guest-init "$GUEST_INIT" --supervisor "$SUPERVISOR" \
  --state-dir "$STATE_DIR" --size-mib 128 --result-port 0 >/dev/null 2>&1 || e2e_fail "create holder"
"$CLI" start holder --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || e2e_fail "start holder"
if run_iso intruder "data:/work" "true" >"$STATE_DIR/intruder.json" 2>&1; then
  e2e_fail "second attach to a running holder should fail"
fi
grep -qi "already attached" "$STATE_DIR/intruder.json" || e2e_log "note: expected 'already attached' message (got other failure, still refused)"
"$CLI" kill holder --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true

e2e_step "rm removes the volume and its backing file"
"$CLI" volume rm data --force --state-dir "$STATE_DIR" >/dev/null || e2e_fail "volume rm"
[ ! -e "$STATE_DIR/volumes/data.ext4" ] || e2e_fail "backing file not removed"

e2e_log "volumes scenario passed"
