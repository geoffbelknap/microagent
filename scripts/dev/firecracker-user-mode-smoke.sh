#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-firecracker-user-network.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
IMAGE="docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f"
EXPECTED_KERNEL_SHA="4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0"

cleanup() {
  status="$?"
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_FIRECRACKER_USER_SMOKE:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept firecracker user-network smoke state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    echo "firecracker user network smoke requires Linux amd64" >&2
    exit 2
    ;;
esac

for required in pasta getcap; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "$required is required for firecracker user network smoke" >&2
    exit 2
  fi
done

if [ ! -e /dev/kvm ]; then
  echo "/dev/kvm is not visible; run this smoke outside sandboxed environments" >&2
  exit 2
fi

if [ ! -e /dev/net/tun ]; then
  echo "/dev/net/tun is not visible; user networking requires tun" >&2
  exit 2
fi

if [ -e /proc/sys/kernel/unprivileged_userns_clone ] && [ "$(cat /proc/sys/kernel/unprivileged_userns_clone)" != "1" ]; then
  echo "kernel.unprivileged_userns_clone is disabled" >&2
  exit 2
fi
if [ -e /proc/sys/user/max_user_namespaces ] && [ "$(cat /proc/sys/user/max_user_namespaces)" = "0" ]; then
  echo "user.max_user_namespaces is 0" >&2
  exit 2
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
  echo "firecracker binary not found; install microagent or set MICROAGENT_FIRECRACKER" >&2
  exit 2
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

if caps="$(getcap "$SUPERVISOR" 2>/dev/null)" && [ -n "$caps" ]; then
  echo "temporary supervisor unexpectedly has file capabilities: $caps" >&2
  exit 1
fi

"$CLI" kernel install --backend firecracker --arch amd64 >"$STATE_DIR/kernel-install.json"
kernel_path="$(python3 - "$STATE_DIR/kernel-install.json" "$EXPECTED_KERNEL_SHA" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)
if result.get("sha256") != sys.argv[2]:
    raise SystemExit(result)
print(result["path"])
PY
)"

"$CLI" run \
  --backend firecracker \
  --image "$IMAGE" \
  --arch amd64 \
  --exec "wget -qO- -T 10 http://example.com >/tmp/user.out && echo USER_OUTBOUND_READY || echo USER_OUTBOUND_FAILED" \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR/user-run" \
  --size-mib 128 \
  --result-port 0 \
  --timeout 30 \
  --network user \
  --keep >"$STATE_DIR/user.json"

python3 - "$STATE_DIR/user.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)
if result["response"]["event"]["state"] != "stopped":
    raise SystemExit(result)
stdout = (result.get("result") or {}).get("stdout") or ""
if "USER_OUTBOUND_FAILED" in stdout:
    raise SystemExit(stdout)
if "USER_OUTBOUND_READY" not in stdout:
    raise SystemExit(stdout)
if result["network"]["mode"] != "user":
    raise SystemExit(result["network"])
PY

echo "firecracker user network smoke passed"
