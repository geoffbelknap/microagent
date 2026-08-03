#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

# Health checks: a workspace declares a guest liveness probe; supervise restarts
# it when unhealthy. Validate the config contract end to end, verify an exec
# probe succeeds in a booted guest, and exercise the supervise health path with
# an unhealthy exec probe that must trigger one restart and exit.
e2e_require_vm
e2e_require_cmd mke2fs "mke2fs is required to build the workspace rootfs"

default_backend() {
  case "$(uname -s):$(uname -m)" in
    Linux:x86_64|Linux:amd64)
      printf '%s\n' linux-kvm
      ;;
    Darwin:arm64)
      printf '%s\n' apple-vf
      ;;
    *)
      printf '%s\n' unsupported
      ;;
  esac
}

BACKEND="$(e2e_normalize_backend "${MICROAGENT_E2E_BACKEND:-$(default_backend)}")"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-health.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR=""
WS="health-ok"
BAD_WS="health-bad-exec"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    for ws in "$WS" "$BAD_WS"; do
      "$CLI" kill "$ws" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" --reason "health E2E cleanup" --yes >/dev/null 2>&1 || true
    done
    if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_HEALTH:-0}" != "1" ]; then
      for ws in "$WS" "$BAD_WS"; do
        "$CLI" delete "$ws" --force --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
      done
    fi
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
if [ -z "${DOCKER_CONFIG:-}" ]; then
  mkdir -p "$STATE_DIR/docker-config"
  export DOCKER_CONFIG="$STATE_DIR/docker-config"
fi

case "$BACKEND" in
  linux-kvm)
    SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
    GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
    IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox:1.36}"
    e2e_build_firecracker_stack "$CLI" "$SUPERVISOR" "$GUEST_INIT"
    "$CLI" kernel install --backend linux-kvm --arch amd64 >"$STATE_DIR/kernel-install.json" 2>/dev/null || e2e_fail "kernel install"
    KERNEL="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["path"])' "$STATE_DIR/kernel-install.json")"
    CREATE_FLAGS=(--kernel "$KERNEL" --guest-init "$GUEST_INIT" --supervisor "$SUPERVISOR" --state-dir "$STATE_DIR" --size-mib 128 --result-port 0)
    START_FLAGS=(--state-dir "$STATE_DIR" --supervisor "$SUPERVISOR")
    ;;
  apple-vf)
    case "$(uname -s):$(uname -m)" in
      Darwin:arm64)
        ;;
      *)
        e2e_skip "Apple VF health E2E requires macOS on Apple silicon"
        ;;
    esac
    SUPERVISOR="${MICROAGENT_APPLEVF_SUPERVISOR:-$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor}"
    KERNEL="${MICROAGENT_APPLEVF_KERNEL:-$HOME/.microagent/kernels/apple-vf/arm64/Image}"
    if [ ! -r "$KERNEL" ] && [ -r "$HOME/.microagent/kernels/apple-vf/Image" ]; then
      KERNEL="$HOME/.microagent/kernels/apple-vf/Image"
    fi
    if [ ! -x "$SUPERVISOR" ]; then
      e2e_skip "supervisor is not executable at $SUPERVISOR; run scripts/dev/applevf-supervisor-build.sh"
    fi
    if [ ! -r "$KERNEL" ]; then
      e2e_skip "kernel is not readable at $KERNEL"
    fi
    ARCH="${MICROAGENT_APPLEVF_BOOT_ARCH:-arm64}"
    IMAGE="${MICROAGENT_APPLEVF_BOOT_IMAGE:-docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6}"
    GUEST_INIT="$STATE_DIR/microagent-guestinit"
    go build -buildvcs=false -o "$CLI" ./cmd/microagent
    GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
    CREATE_FLAGS=(--backend apple-vf --kernel "$KERNEL" --guest-init "$GUEST_INIT" --supervisor "$SUPERVISOR" --state-dir "$STATE_DIR" --size-mib 128 --result-port 0)
    START_FLAGS=(--state-dir "$STATE_DIR" --supervisor "$SUPERVISOR")
    ;;
  *)
    e2e_skip "health E2E does not support backend lane: $BACKEND"
    ;;
esac

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

cat >"$STATE_DIR/unhealthy.yaml" <<YAML
name: $BAD_WS
image: $IMAGE
restart: on-failure
network: { mode: isolated }
service: sleep 600
health:
  exec: ["/bin/false"]
  intervalSeconds: 1
  timeoutSeconds: 2
  retries: 1
  startPeriodSeconds: 0
YAML

e2e_step "invalid health spec (both exec and httpGet) is rejected"
if "$CLI" create --file "$STATE_DIR/invalid.yaml" --dry-run "${CREATE_FLAGS[@]}" >"$STATE_DIR/invalid.json" 2>&1; then
  e2e_fail "expected invalid health spec to be rejected"
fi

e2e_step "valid health spec builds and boots"
"$CLI" create --file "$STATE_DIR/healthy.yaml" "${CREATE_FLAGS[@]}" >/dev/null 2>&1 || e2e_fail "create with health"
"$CLI" start "$WS" "${START_FLAGS[@]}" >/dev/null 2>&1 || e2e_fail "start"
e2e_wait_exec_ready "$CLI" "$STATE_DIR" "$WS" || e2e_fail "exec service never became ready"

e2e_step "the declared exec probe succeeds in the booted guest"
"$CLI" exec "$WS" --state-dir "$STATE_DIR" -- /bin/true >/dev/null 2>&1 \
  || e2e_fail "health probe command failed in guest"

e2e_step "status reports the running workspace"
"$CLI" --json status "$WS" --state-dir "$STATE_DIR" | grep -q '"running"' \
  || e2e_fail "workspace not running"

e2e_step "supervise restarts an unhealthy exec probe"
"$CLI" create --file "$STATE_DIR/unhealthy.yaml" "${CREATE_FLAGS[@]}" >/dev/null 2>&1 || e2e_fail "create unhealthy health workspace"
SUPERVISE_OUT="$STATE_DIR/supervise-unhealthy.out"
"$CLI" supervise "$BAD_WS" "${START_FLAGS[@]}" --interval 1 --max-restarts 1 >"$SUPERVISE_OUT" 2>&1 &
supervise_pid="$!"
supervise_status=0
supervise_wait=0
while kill -0 "$supervise_pid" 2>/dev/null; do
  if [ "$supervise_wait" -ge 60 ]; then
    kill "$supervise_pid" 2>/dev/null || true
    wait "$supervise_pid" 2>/dev/null || true
    cat "$SUPERVISE_OUT" >&2
    e2e_fail "supervise did not exit after unhealthy health probe"
  fi
  sleep 1
  supervise_wait=$((supervise_wait + 1))
done
wait "$supervise_pid" || supervise_status="$?"
if [ "$supervise_status" -ne 0 ]; then
  cat "$SUPERVISE_OUT" >&2
  e2e_fail "supervise unhealthy workspace"
fi
grep -Eq 'Restarts: 1|"restarts": 1' "$SUPERVISE_OUT" \
  || { cat "$SUPERVISE_OUT" >&2; e2e_fail "supervise did not report one health-triggered restart"; }
grep -Eq 'Final state: failed|"final_state": "failed"' "$SUPERVISE_OUT" \
  || { cat "$SUPERVISE_OUT" >&2; e2e_fail "supervise did not report failed health state"; }

e2e_log "health scenario passed"
