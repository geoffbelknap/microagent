#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

# Health checks: a workspace declares a guest liveness probe; supervise restarts
# it when unhealthy. Here we validate the config contract end to end — a valid
# health spec builds and boots, an invalid one is rejected — and that an exec
# probe command actually succeeds in the booted guest. (Restart-on-unhealthy
# timing is covered by unit tests for the health tracker.)
e2e_require_vm
e2e_require_cmd mke2fs "mke2fs is required to build the workspace rootfs"

STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-health.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox:1.36}"
WS="health-ok"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    "$CLI" kill "$WS" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" delete "$WS" --force --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_HEALTH:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E health state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

cd "$ROOT"
export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
e2e_build_firecracker_stack "$CLI" "$SUPERVISOR" "$GUEST_INIT"
"$CLI" kernel install --backend firecracker --arch amd64 >"$STATE_DIR/kernel-install.json" 2>/dev/null || e2e_fail "kernel install"
kernel_path="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["path"])' "$STATE_DIR/kernel-install.json")"

cat >"$STATE_DIR/healthy.yaml" <<YAML
name: $WS
image: $IMAGE
network: { mode: isolated }
service: sleep 600
health:
  exec: ["/bin/true"]
  intervalSeconds: 5
  timeoutSeconds: 2
  retries: 3
  startPeriodSeconds: 1
YAML

cat >"$STATE_DIR/invalid.yaml" <<YAML
name: health-bad
image: $IMAGE
network: { mode: isolated }
health:
  exec: ["/bin/true"]
  httpGet: /healthz
  port: 8080
YAML

e2e_step "invalid health spec (both exec and httpGet) is rejected"
if "$CLI" create --file "$STATE_DIR/invalid.yaml" --dry-run --state-dir "$STATE_DIR" >"$STATE_DIR/invalid.json" 2>&1; then
  e2e_fail "expected invalid health spec to be rejected"
fi

e2e_step "valid health spec builds and boots"
"$CLI" create --file "$STATE_DIR/healthy.yaml" --kernel "$kernel_path" --guest-init "$GUEST_INIT" \
  --supervisor "$SUPERVISOR" --state-dir "$STATE_DIR" --size-mib 128 --result-port 0 >/dev/null 2>&1 || e2e_fail "create with health"
"$CLI" start "$WS" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || e2e_fail "start"
e2e_wait_exec_ready "$CLI" "$STATE_DIR" "$WS" || e2e_fail "exec service never became ready"

e2e_step "the declared exec probe succeeds in the booted guest"
"$CLI" exec "$WS" --state-dir "$STATE_DIR" -- /bin/true >/dev/null 2>&1 \
  || e2e_fail "health probe command failed in guest"

e2e_step "status reports the running workspace"
"$CLI" --json status "$WS" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" | grep -q '"running"' \
  || e2e_fail "workspace not running"

e2e_log "health scenario passed"
