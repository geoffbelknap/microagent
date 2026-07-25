#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

# Streaming structured exec (exec --stream): the guest delivers stdout/stderr as
# incremental chunks followed by a result frame. We assert all streamed lines
# arrive and the command's exit status is honored.
e2e_require_vm
e2e_require_cmd mke2fs "mke2fs is required to build the workspace rootfs"

default_backend() {
  case "$(uname -s):$(uname -m)" in
    Linux:x86_64|Linux:amd64)
      printf '%s\n' linux-kvm
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
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-exec-stream.XXXXXX")"
CLI="$STATE_DIR/microagent"
WS="exec-stream"
SUPERVISOR=""

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    if [ -n "$SUPERVISOR" ]; then
      "$CLI" kill "$WS" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
      if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_EXEC_STREAM:-0}" != "1" ]; then
        "$CLI" delete "$WS" --force --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
      fi
    else
      "$CLI" kill "$WS" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_EXEC_STREAM:-0}" != "1" ]; then
        "$CLI" delete "$WS" --force --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      fi
    fi
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_EXEC_STREAM:-0}" != "1" ]; then
    if ! rm -rf "$STATE_DIR"; then
      sleep 1
      chmod -R u+w "$STATE_DIR" 2>/dev/null || true
      rm -rf "$STATE_DIR" 2>/dev/null || echo "warning: could not fully remove exec-stream state at $STATE_DIR" >&2
    fi
  else
    echo "kept microagent E2E exec-stream state at $STATE_DIR" >&2
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
    ARCH="amd64"
    e2e_build_firecracker_stack "$CLI" "$SUPERVISOR" "$GUEST_INIT"
    "$CLI" kernel install --backend linux-kvm --arch "$ARCH" >"$STATE_DIR/kernel-install.json" 2>/dev/null || e2e_fail "kernel install"
    KERNEL="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["path"])' "$STATE_DIR/kernel-install.json")"
    run_cli() { "$CLI" "$@" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR"; }
    create_workspace() {
      run_cli create --name "$WS" --image "$IMAGE" --network isolated --service-command "sleep 600" \
        --kernel "$KERNEL" --guest-init "$GUEST_INIT" --size-mib 128 --result-port 0 >"$STATE_DIR/create.json" 2>&1
    }
    ;;
  applevf)
    case "$(uname -s):$(uname -m)" in
      Darwin:arm64)
        ;;
      *)
        e2e_skip "Apple VF exec-stream E2E requires macOS on Apple silicon"
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
    run_cli() { "$CLI" "$@" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR"; }
    create_workspace() {
      run_cli create --name "$WS" --image "$IMAGE" --backend apple-vf --network isolated --service-command "sleep 600" \
        --kernel "$KERNEL" --guest-init "$GUEST_INIT" --size-mib 128 --result-port 0 >"$STATE_DIR/create.json" 2>&1
    }
    ;;
  *)
    e2e_skip "exec-stream E2E does not support backend lane: $BACKEND"
    ;;
esac

# exec connects to the running guest directly and does not take --supervisor.
exec_ws() { ws="$1"; shift; "$CLI" exec "$ws" --state-dir "$STATE_DIR" "$@"; }

create_workspace || { cat "$STATE_DIR/create.json"; e2e_fail "create"; }
run_cli start "$WS" >/dev/null 2>&1 || e2e_fail "start"
e2e_wait_exec_ready "$CLI" "$STATE_DIR" "$WS" "${MICROAGENT_E2E_WAIT_TIMEOUT:-60}" || e2e_fail "exec service never became ready"

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
