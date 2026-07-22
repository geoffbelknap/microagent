#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

SUPERVISOR="${MICROAGENT_APPLEVF_SUPERVISOR:-$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor}"
KERNEL="${MICROAGENT_APPLEVF_KERNEL:-$HOME/.microagent/kernels/apple-vf/arm64/Image}"
if [ ! -r "$KERNEL" ] && [ -r "$HOME/.microagent/kernels/apple-vf/Image" ]; then
  KERNEL="$HOME/.microagent/kernels/apple-vf/Image"
fi
IMAGE="${MICROAGENT_APPLEVF_BOOT_IMAGE:-docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6}"
ARCH="${MICROAGENT_APPLEVF_BOOT_ARCH:-arm64}"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-applevf-snapshot.XXXXXX")"
WORKSPACE="snapshot-smoke"
FORK="snapshot-fork"
CLI="$STATE_DIR/microagent"
GUEST_INIT="$STATE_DIR/microagent-guestinit"

cleanup() {
  status="$?"
  "$CLI" kill "$FORK" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  "$CLI" kill "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_APPLEVF_SNAPSHOT_SMOKE:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept Apple VF snapshot smoke state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Darwin:arm64) ;;
  *) e2e_skip "Apple VF snapshot smoke requires macOS on Apple silicon" ;;
esac

if [ ! -r "$KERNEL" ]; then
  e2e_skip "kernel is not readable at $KERNEL"
fi
if [ ! -x "$SUPERVISOR" ]; then
  e2e_skip "supervisor is not executable at $SUPERVISOR; run scripts/dev/applevf-supervisor-build.sh"
fi

if command -v mke2fs >/dev/null 2>&1; then
  MKE2FS="$(command -v mke2fs)"
elif [ -x /opt/homebrew/opt/e2fsprogs/sbin/mke2fs ]; then
  MKE2FS="/opt/homebrew/opt/e2fsprogs/sbin/mke2fs"
else
  e2e_skip "mke2fs not found; install e2fsprogs"
fi

(
  cd "$ROOT"
  go build -o "$CLI" ./cmd/microagent
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

exec_ws() {
  ws="$1"
  shift
  "$CLI" exec "$ws" --state-dir "$STATE_DIR" -- "$@"
}

assert_file() {
  path="$1"
  [ -e "$path" ] || e2e_fail "expected file $path"
}

"$CLI" create "$WORKSPACE" \
  --backend apple-vf \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --size-mib "${MICROAGENT_APPLEVF_BOOT_SIZE_MIB:-128}" \
  --mke2fs "$MKE2FS" \
  --kernel "$KERNEL" \
  --state-dir "$STATE_DIR" \
  --memory "${MICROAGENT_APPLEVF_BOOT_MEMORY_MIB:-512}" \
  --cpus "${MICROAGENT_APPLEVF_BOOT_CPUS:-2}" \
  --timeout "${MICROAGENT_APPLEVF_BOOT_TIMEOUT_SECONDS:-30}" \
  --guest-init "$GUEST_INIT" \
  --supervisor "$SUPERVISOR" >"$STATE_DIR/create.json"

"$CLI" start "$WORKSPACE" \
  --backend apple-vf \
  --state-dir "$STATE_DIR" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" >"$STATE_DIR/start.json"
e2e_wait_exec_ready "$CLI" "$STATE_DIR" "$WORKSPACE" 90 || e2e_fail "source workspace did not become exec-ready"

exec_ws "$WORKSPACE" sh -c 'printf snapshot-source > /snapshot-marker; sync' >"$STATE_DIR/write-source.out"

"$CLI" snapshot create "$WORKSPACE" \
  --backend apple-vf \
  --tag baseline \
  --state-dir "$STATE_DIR" \
  --supervisor "$SUPERVISOR" >"$STATE_DIR/snapshot-create.out"

SNAPSHOT_DIR="$STATE_DIR/$WORKSPACE/snapshots/baseline"
assert_file "$SNAPSHOT_DIR/manifest.json"
assert_file "$SNAPSHOT_DIR/rootfs.ext4"
assert_file "$SNAPSHOT_DIR/machine-state.vz"
assert_file "$SNAPSHOT_DIR/apple-vf-config.json"
python3 - "$SNAPSHOT_DIR/manifest.json" <<'PY'
import json
import sys
from pathlib import Path

manifest = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
if manifest.get("rootfsArtifact") != "rootfs.ext4":
    raise SystemExit(f"unexpected rootfs artifact: {manifest.get('rootfsArtifact')}")
artifacts = manifest.get("machineStateArtifacts") or []
for expected in (
    {"kind": "apple-vf-machine-state", "path": "machine-state.vz"},
    {"kind": "apple-vf-restore-config", "path": "apple-vf-config.json"},
):
    if expected not in artifacts:
        raise SystemExit(f"missing Apple VF artifact {expected}: {artifacts}")
if manifest.get("tag") != "baseline":
    raise SystemExit(f"unexpected tag: {manifest.get('tag')}")
PY

# Re-snapshot the same tag: overwrite must succeed and the second capture must
# win (temp-swap publish, aligned with the Firecracker backend). The restore
# and fork checks below then assert against the SECOND capture's marker, so a
# stale first capture surviving at the tag fails the smoke.
exec_ws "$WORKSPACE" sh -c 'printf overwrite-source > /snapshot-marker; sync' >"$STATE_DIR/write-overwrite.out"
"$CLI" snapshot create "$WORKSPACE" \
  --backend apple-vf \
  --tag baseline \
  --state-dir "$STATE_DIR" \
  --supervisor "$SUPERVISOR" >"$STATE_DIR/snapshot-overwrite.out"
assert_file "$SNAPSHOT_DIR/manifest.json"
assert_file "$SNAPSHOT_DIR/rootfs.ext4"
assert_file "$SNAPSHOT_DIR/machine-state.vz"
assert_file "$SNAPSHOT_DIR/apple-vf-config.json"
if [ -n "$(ls -A "$STATE_DIR/$WORKSPACE/.snapshot-staging" 2>/dev/null)" ]; then
  e2e_fail "snapshot staging residue left behind after overwrite"
fi

exec_ws "$WORKSPACE" sh -c 'printf mutated-source > /snapshot-marker; sync' >"$STATE_DIR/mutate-source.out"
"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt-source.json"
"$CLI" start "$WORKSPACE" \
  --backend apple-vf \
  --from-snapshot baseline \
  --state-dir "$STATE_DIR" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" >"$STATE_DIR/restore-source.json"
e2e_wait_exec_ready "$CLI" "$STATE_DIR" "$WORKSPACE" 90 || e2e_fail "restored source workspace did not become exec-ready"
exec_ws "$WORKSPACE" sh -c 'cat /snapshot-marker' >"$STATE_DIR/restore-marker.out"
grep -q "overwrite-source" "$STATE_DIR/restore-marker.out" || e2e_fail "restore did not roll back to the re-snapshotted (second) capture"

"$CLI" create "$FORK" \
  --backend apple-vf \
  --from-snapshot "$WORKSPACE:baseline" \
  --state-dir "$STATE_DIR" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" >"$STATE_DIR/fork-create.json"
e2e_wait_exec_ready "$CLI" "$STATE_DIR" "$FORK" 90 || e2e_fail "fork workspace did not become exec-ready"
exec_ws "$FORK" sh -c 'cat /snapshot-marker' >"$STATE_DIR/fork-marker.out"
grep -q "overwrite-source" "$STATE_DIR/fork-marker.out" || e2e_fail "fork did not resume from snapshot marker"

exec_ws "$FORK" sh -c 'printf fork-only > /snapshot-marker; sync' >"$STATE_DIR/mutate-fork.out"
exec_ws "$WORKSPACE" sh -c 'cat /snapshot-marker' >"$STATE_DIR/source-after-fork.out"
grep -q "overwrite-source" "$STATE_DIR/source-after-fork.out" || e2e_fail "fork mutation changed source workspace"
exec_ws "$FORK" sh -c 'cat /snapshot-marker' >"$STATE_DIR/fork-after-mutation.out"
grep -q "fork-only" "$STATE_DIR/fork-after-mutation.out" || e2e_fail "fork mutation did not persist in fork"

"$CLI" snapshot list "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/snapshot-list.out"
grep -q "baseline" "$STATE_DIR/snapshot-list.out" || e2e_fail "snapshot list did not include baseline"

echo "Apple VF snapshot create/overwrite/restore/fork smoke passed"
