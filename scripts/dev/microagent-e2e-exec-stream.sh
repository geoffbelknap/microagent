#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

# Streaming structured exec (exec --stream): the guest delivers stdout/stderr as
# incremental chunks followed by a result frame. We assert all streamed lines
# arrive and the command's exit status is honored.
e2e_require_vm
e2e_require_cmd mke2fs "mke2fs is required to build the workspace rootfs"

STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-exec-stream.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox:1.36}"
WS="exec-stream"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    "$CLI" kill "$WS" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" delete "$WS" --force --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_EXEC_STREAM:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E exec-stream state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

cd "$ROOT"
export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
e2e_build_firecracker_stack "$CLI" "$SUPERVISOR" "$GUEST_INIT"
"$CLI" kernel install --backend firecracker --arch amd64 >"$STATE_DIR/kernel-install.json" 2>/dev/null || e2e_fail "kernel install"
kernel_path="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["path"])' "$STATE_DIR/kernel-install.json")"

run_cli() { "$CLI" "$@" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR"; }
# exec connects to the running guest directly and does not take --supervisor.
exec_ws() { ws="$1"; shift; "$CLI" exec "$ws" --state-dir "$STATE_DIR" "$@"; }

run_cli create "$WS" --image "$IMAGE" --network isolated --service-command "sleep 600" \
  --kernel "$kernel_path" --guest-init "$GUEST_INIT" --size-mib 128 --result-port 0 >/dev/null 2>&1 || e2e_fail "create"
run_cli start "$WS" >/dev/null 2>&1 || e2e_fail "start"
e2e_wait_exec_ready "$CLI" "$STATE_DIR" "$WS" || e2e_fail "exec service never became ready"

e2e_step "exec --stream delivers all stdout lines"
# shellcheck disable=SC2016  # $i is expanded by the guest shell, not the host
exec_ws "$WS" --stream -- sh -c 'for i in 1 2 3; do echo line$i; done' >"$STATE_DIR/stream.out" 2>&1 || { cat "$STATE_DIR/stream.out"; e2e_fail "exec --stream"; }
cat "$STATE_DIR/stream.out"
for i in 1 2 3; do
  grep -q "line$i" "$STATE_DIR/stream.out" || e2e_fail "streamed output missing line$i"
done

e2e_step "exec --stream honors a non-zero exit status"
if exec_ws "$WS" --stream -- sh -c 'echo before; exit 7' >"$STATE_DIR/stream-exit.out" 2>&1; then
  e2e_fail "exec --stream should propagate non-zero exit"
fi
grep -q "before" "$STATE_DIR/stream-exit.out" || e2e_fail "streamed output missing pre-exit line"

e2e_step "streamed and buffered exec agree on output"
exec_ws "$WS" -- echo parity-check >"$STATE_DIR/buf.out" 2>&1 || e2e_fail "buffered exec"
grep -q "parity-check" "$STATE_DIR/buf.out" || e2e_fail "buffered exec output missing"

e2e_log "exec-stream scenario passed"
