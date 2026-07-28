#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

# persistence across runs, and single-attach enforcement. The attach tests boot
# isolated microVMs (no network), so this needs a VM backend. The ext4 lanes
# so it needs no mke2fs.
e2e_require_vm
e2e_require_cmd mke2fs "mke2fs (e2fsprogs) is required to format named volumes"

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
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-volumes.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR=""

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    "$CLI" kill holder --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_VOLUMES:-0}" != "1" ]; then
      "$CLI" delete holder --force --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    fi
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
    RUN_FLAGS=(--kernel "$KERNEL" --guest-init "$GUEST_INIT" --supervisor "$SUPERVISOR" --state-dir "$STATE_DIR" --size-mib 128 --result-port 0)
    START_FLAGS=(--state-dir "$STATE_DIR" --supervisor "$SUPERVISOR")
    ;;
  applevf)
    case "$(uname -s):$(uname -m)" in
      Darwin:arm64)
        ;;
      *)
        e2e_skip "Apple VF volumes E2E requires macOS on Apple silicon"
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
    RUN_FLAGS=(--backend apple-vf --kernel "$KERNEL" --guest-init "$GUEST_INIT" --supervisor "$SUPERVISOR" --state-dir "$STATE_DIR" --size-mib 128 --result-port 0)
    START_FLAGS=(--state-dir "$STATE_DIR" --supervisor "$SUPERVISOR")
    ;;
  *)
    e2e_skip "volumes E2E does not support backend lane: $BACKEND"
    ;;
esac

[ -r "$KERNEL" ] || e2e_fail "kernel is not readable at $KERNEL"

VOLUME_BACKING="$STATE_DIR/volumes/data.ext4"

# Git Bash mangles the guest mountpoint inside a colon-separated volume spec
run_iso() { # run_iso <name-hint> <volume-spec> <exec>
  "$CLI" run --image "$IMAGE" --network isolated \
    "${RUN_FLAGS[@]}" --timeout 40 --keep --volume "$2" --exec "$3"
}

e2e_step "volume create / ls / inspect"
"$CLI" volume create data --size-mib 32 --state-dir "$STATE_DIR" >/dev/null || e2e_fail "volume create"
"$CLI" volume list --state-dir "$STATE_DIR" | grep -q "data" || e2e_fail "volume list missing data"
"$CLI" --json volume status data --state-dir "$STATE_DIR" | grep -q '"size_mib": 32' || e2e_fail "volume status size"
[ -f "$VOLUME_BACKING" ] || e2e_fail "backing file not built at $VOLUME_BACKING"
file "$VOLUME_BACKING" | grep -qi "ext4 filesystem" || e2e_fail "backing file is not ext4"

e2e_step "duplicate and invalid names are rejected"
if "$CLI" volume create data --state-dir "$STATE_DIR" >/dev/null 2>&1; then e2e_fail "duplicate volume allowed"; fi
if "$CLI" volume create Bad_Name --state-dir "$STATE_DIR" >/dev/null 2>&1; then e2e_fail "invalid name allowed"; fi

e2e_step "attach-by-name persists data across separate runs"
run_iso writer "data:/work" "echo persisted-ok > /work/marker" >"$STATE_DIR/w1.json" 2>&1 || { cat "$STATE_DIR/w1.json"; e2e_fail "write run"; }
run_iso reader "data:/work" "cat /work/marker" >"$STATE_DIR/w2.json" 2>&1 || { cat "$STATE_DIR/w2.json"; e2e_fail "read run"; }
grep -q "persisted-ok" "$STATE_DIR/w2.json" || e2e_fail "volume did not persist data across runs"

e2e_step "single-attach: a running holder blocks a second attach"
"$CLI" create holder --image "$IMAGE" --network isolated --service-command "sleep 120" \
  --volume data:/work "${CREATE_FLAGS[@]}" >/dev/null 2>&1 || e2e_fail "create holder"
"$CLI" start holder "${START_FLAGS[@]}" >/dev/null 2>&1 || e2e_fail "start holder"
if run_iso intruder "data:/work" "true" >"$STATE_DIR/intruder.json" 2>&1; then
  e2e_fail "second attach to a running holder should fail"
fi
grep -qi "already attached" "$STATE_DIR/intruder.json" || e2e_log "note: expected 'already attached' message (got other failure, still refused)"
"$CLI" kill holder "${START_FLAGS[@]}" >/dev/null 2>&1 || true

e2e_step "rm removes the volume and its backing file"
"$CLI" volume delete data --force --state-dir "$STATE_DIR" >/dev/null || e2e_fail "volume delete"
[ ! -e "$VOLUME_BACKING" ] || e2e_fail "backing file not removed"

e2e_log "volumes scenario passed"
