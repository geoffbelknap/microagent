#!/usr/bin/env bash
set -euo pipefail

# Boots a rootless *confined* Firecracker workspace (MICROAGENT_CONFINEMENT=rootless)
# and asserts the confinement actually engaged: the run dir holds a populated jail
# (hard-linked artifacts) and the emitted firecracker.json references jail-relative
# paths, which only resolve after pivot_root — so a successful boot through them
# proves the --confined-exec re-exec ran. Gates the on-by-default flip.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-firecracker-confined.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
IMAGE="docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f"

cleanup() {
  status="$?"
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_FIRECRACKER_CONFINED_SMOKE:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept firecracker confined smoke state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    e2e_skip "firecracker confined smoke requires Linux amd64"
    ;;
esac

for required in pasta getcap; do
  if ! command -v "$required" >/dev/null 2>&1; then
    e2e_skip "$required is required for firecracker confined smoke"
  fi
done

if [ ! -e /dev/kvm ]; then
  e2e_skip "/dev/kvm is not visible; run this smoke outside sandboxed environments"
fi

if [ ! -e /dev/net/tun ]; then
  e2e_skip "/dev/net/tun is not visible; user networking requires tun"
fi

if [ -e /proc/sys/kernel/unprivileged_userns_clone ] && [ "$(cat /proc/sys/kernel/unprivileged_userns_clone)" != "1" ]; then
  e2e_skip "kernel.unprivileged_userns_clone is disabled; rootless confinement needs user namespaces"
fi
if [ -e /proc/sys/user/max_user_namespaces ] && [ "$(cat /proc/sys/user/max_user_namespaces)" = "0" ]; then
  e2e_skip "user.max_user_namespaces is 0; rootless confinement needs user namespaces"
fi

if [ -n "${MICROAGENT_FIRECRACKER:-}" ]; then
  firecracker="$MICROAGENT_FIRECRACKER"
elif command -v firecracker >/dev/null 2>&1; then
  firecracker="$(command -v firecracker)"
elif command -v brew >/dev/null 2>&1; then
  formula_prefix="$(brew --prefix microagent 2>/dev/null || true)"
  firecracker="$formula_prefix/libexec/firecracker"
else
  firecracker=""
fi

if [ ! -x "${firecracker:-}" ]; then
  e2e_skip "firecracker binary not found; install microagent or set MICROAGENT_FIRECRACKER"
fi

export GOCACHE="$STATE_DIR/gocache"
export GOMODCACHE="$STATE_DIR/gomodcache"
export GOFLAGS="${GOFLAGS:-} -modcacherw"
export MICROAGENT_FIRECRACKER="$firecracker"
export MICROAGENT_FIRECRACKER_SUPERVISOR="$SUPERVISOR"

(
  cd "$ROOT"
  go build -buildvcs=false -o "$CLI" ./cmd/microagent
  go build -buildvcs=false -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

"$CLI" kernel install --backend linux-kvm --arch amd64 >"$STATE_DIR/kernel-install.json"
kernel_path="$(python3 - "$STATE_DIR/kernel-install.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)
print(result["path"])
PY
)"

RUN_STATE="$STATE_DIR/confined-run"
MICROAGENT_CONFINEMENT=rootless "$CLI" run \
  --backend linux-kvm \
  --image "$IMAGE" \
  --arch amd64 \
  --exec "echo CONFINED_SMOKE_OK" \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$RUN_STATE" \
  --size-mib 128 \
  --result-port 0 \
  --timeout 30 \
  --network user \
  --keep >"$STATE_DIR/confined.json"

python3 - "$STATE_DIR/confined.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)
if result["response"]["event"]["state"] != "stopped":
    raise SystemExit(result)
stdout = (result.get("result") or {}).get("stdout") or ""
if "CONFINED_SMOKE_OK" not in stdout:
    raise SystemExit(stdout)
PY

run_dir="$(find "$RUN_STATE" -maxdepth 1 -type d -name 'run-*' | head -1)"
if [ -z "$run_dir" ]; then
  echo "no run-* directory under $RUN_STATE" >&2
  exit 1
fi
for art in jail/kernel jail/rootfs.ext4 jail/firecracker; do
  if [ ! -e "$run_dir/$art" ]; then
    echo "confined jail missing $art:" >&2
    ls -la "$run_dir/jail" >&2 || true
    exit 1
  fi
done

python3 - "$run_dir/firecracker.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    cfg = json.load(f)
kernel = cfg["boot-source"]["kernel_image_path"]
rootfs = cfg["drives"][0]["path_on_host"]
if kernel != "/kernel":
    raise SystemExit(f"kernel_image_path={kernel!r}, want /kernel (jail-relative)")
if rootfs != "/rootfs.ext4":
    raise SystemExit(f"rootfs path_on_host={rootfs!r}, want /rootfs.ext4 (jail-relative)")
PY

echo "firecracker confined smoke passed"
