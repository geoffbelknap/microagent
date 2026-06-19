#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

# commit snapshots a stopped workspace rootfs back into an OCI image in the local
# image layout (the reverse of OCI->rootfs). Registry push is not exercised here
# (no registry dependency); the assemble + local-store path is.
e2e_require_vm
if ! e2e_is_windows; then
  # The ext4 lanes extract with debugfs; windows-hyperv commits through a
  # guest maintenance boot over the exec channel instead.
  e2e_require_cmd debugfs "debugfs (e2fsprogs) is required for unprivileged rootfs extraction"
  e2e_require_cmd mke2fs "mke2fs is required to build the workspace rootfs"
fi

default_backend() {
  case "$(uname -s):$(uname -m)" in
    Linux:x86_64|Linux:amd64)
      printf '%s\n' linux-kvm
      ;;
    Darwin:arm64)
      printf '%s\n' applevf
      ;;
    MINGW*:x86_64|MSYS*:x86_64|CYGWIN*:x86_64)
      printf '%s\n' windows-hyperv
      ;;
    *)
      printf '%s\n' unsupported
      ;;
  esac
}

BACKEND="${MICROAGENT_E2E_BACKEND:-$(default_backend)}"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-commit.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR=""
WS="commit-src"
REF="local/microagent-e2e:committed"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    if [ -n "$SUPERVISOR" ]; then
      "$CLI" kill "$WS" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    else
      "$CLI" kill "$WS" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    fi
    if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_COMMIT:-0}" != "1" ]; then
      if [ -n "$SUPERVISOR" ]; then
        "$CLI" delete "$WS" --force --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
      else
        "$CLI" delete "$WS" --force --yes --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      fi
    fi
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_COMMIT:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E commit state at $STATE_DIR" >&2
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
    CREATE_FLAGS=(--kernel "$KERNEL" --guest-init "$GUEST_INIT" --size-mib 128 --result-port 0)
    ;;
  applevf)
    case "$(uname -s):$(uname -m)" in
      Darwin:arm64)
        ;;
      *)
        e2e_skip "Apple VF commit-images E2E requires macOS on Apple silicon"
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
    CREATE_FLAGS=(--backend apple-vf --kernel "$KERNEL" --guest-init "$GUEST_INIT" --size-mib 128 --result-port 0)
    ;;
  windows-hyperv)
    e2e_is_windows || e2e_skip "windows-hyperv commit-images E2E requires a Windows host"
    e2e_have_hcs || e2e_skip "Hyper-V HCS services (vmms/vmcompute) are not running"
    CLI="$STATE_DIR/microagent.exe"
    GUEST_INIT="$STATE_DIR/microagent-guestinit"
    IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox:1.36}"
    go build -buildvcs=false -o "$CLI" ./cmd/microagent
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
    KERNEL="$HOME/.microagent/kernels/windows-hyperv/amd64/Image"
    if [ ! -r "$KERNEL" ]; then
      "$CLI" kernel install || e2e_skip "windows-hyperv kernel install failed"
    fi
    # 512 MiB: the busybox VHD build needs the headroom; no supervisor
    # binary exists on this backend.
    CREATE_FLAGS=(--size-mib 512)
    ;;
  *)
    e2e_skip "commit-images E2E does not support backend lane: $BACKEND"
    ;;
esac

run_cli() {
  if [ -n "$SUPERVISOR" ]; then
    "$CLI" "$@" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR"
  else
    "$CLI" "$@" --state-dir "$STATE_DIR"
  fi
}
# exec and commit operate without a supervisor handle.
exec_ws() { ws="$1"; shift; "$CLI" exec "$ws" --state-dir "$STATE_DIR" "$@"; }
commit_ws() { "$CLI" commit "$@" --state-dir "$STATE_DIR"; }

e2e_step "boot a workspace and write a marker into its rootfs"
run_cli create "$WS" --image "$IMAGE" --network isolated --service-command "sleep 600" \
  "${CREATE_FLAGS[@]}" >/dev/null 2>&1 || e2e_fail "create"
run_cli start "$WS" >/dev/null 2>&1 || e2e_fail "start"
e2e_wait_exec_ready "$CLI" "$STATE_DIR" "$WS" || e2e_fail "exec service never became ready"
exec_ws "$WS" -- sh -c 'echo committed-marker > /root/marker' >/dev/null 2>&1 || e2e_fail "write marker"

e2e_step "halt, then commit the stopped rootfs into an OCI image"
run_cli halt "$WS" >/dev/null 2>&1 || e2e_fail "halt"
commit_ws "$WS" "$REF" >"$STATE_DIR/commit.json" 2>&1 || { cat "$STATE_DIR/commit.json"; e2e_fail "commit"; }

e2e_step "committed image lands in the local OCI layout"
layout="$STATE_DIR/images/oci"
[ -f "$layout/index.json" ] || e2e_fail "no OCI layout index at $layout"
grep -q "local/microagent-e2e" "$layout/index.json" || e2e_fail "committed reference not in OCI index"
# An image manifest + at least one blob should exist.
if [ ! -d "$layout/blobs/sha256" ] || [ -z "$(ls -A "$layout/blobs/sha256" 2>/dev/null)" ]; then
  e2e_fail "no blobs written for committed image"
fi

e2e_step "commit refuses a running workspace"
run_cli start "$WS" >/dev/null 2>&1 || e2e_fail "restart for refusal check"
if commit_ws "$WS" "$REF" >"$STATE_DIR/commit-running.json" 2>&1; then
  e2e_fail "commit should refuse a running workspace"
fi
run_cli kill "$WS" >/dev/null 2>&1 || true

e2e_log "commit-images scenario passed"
