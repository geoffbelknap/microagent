#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-firecracker-user-network.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
IMAGE="docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f"

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
    e2e_skip "firecracker user network smoke requires Linux amd64"
    ;;
esac

for required in pasta getcap; do
  if ! command -v "$required" >/dev/null 2>&1; then
    e2e_skip "$required is required for firecracker user network smoke"
  fi
done

if [ ! -e /dev/kvm ]; then
  e2e_skip "/dev/kvm is not visible; run this smoke outside sandboxed environments"
fi

if [ ! -e /dev/net/tun ]; then
  e2e_skip "/dev/net/tun is not visible; user networking requires tun"
fi

if [ -e /proc/sys/kernel/unprivileged_userns_clone ] && [ "$(cat /proc/sys/kernel/unprivileged_userns_clone)" != "1" ]; then
  e2e_skip "kernel.unprivileged_userns_clone is disabled"
fi
if [ -e /proc/sys/user/max_user_namespaces ] && [ "$(cat /proc/sys/user/max_user_namespaces)" = "0" ]; then
  e2e_skip "user.max_user_namespaces is 0"
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

if caps="$(getcap "$SUPERVISOR" 2>/dev/null)" && [ -n "$caps" ]; then
  echo "temporary supervisor unexpectedly has file capabilities: $caps" >&2
  exit 1
fi

"$CLI" kernel install --backend linux-kvm --arch amd64 >"$STATE_DIR/kernel-install.json"
kernel_path="$(python3 - "$STATE_DIR/kernel-install.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)
print(result["path"])
PY
)"

"$CLI" run \
  --backend linux-kvm \
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

# Regression: companion processes (vsock listener, port forwarder) must not
# outlive a guest that exits on its own, and the published port must be
# released without any explicit lifecycle verb.
SELF_EXIT_STATE="$STATE_DIR/self-exit"
PUBLISH_PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"

"$CLI" create self-exit \
  --backend linux-kvm \
  --image "$IMAGE" \
  --arch amd64 \
  --entrypoint "sleep 5" \
  --publish "$PUBLISH_PORT:8099" \
  --network user \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --size-mib 128 \
  --state-dir "$SELF_EXIT_STATE" >"$STATE_DIR/self-exit-create.json"

"$CLI" start self-exit --state-dir "$SELF_EXIT_STATE" >"$STATE_DIR/self-exit-start.json"

deadline="$((SECONDS + 90))"
while true; do
  "$CLI" status self-exit --state-dir "$SELF_EXIT_STATE" >"$STATE_DIR/self-exit-status.json" || true
  state="$(python3 -c 'import json,sys; print((json.load(open(sys.argv[1])).get("event") or {}).get("state",""))' "$STATE_DIR/self-exit-status.json")"
  if [ "$state" = "stopped" ] || [ "$state" = "failed" ]; then
    break
  fi
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "self-exit workspace did not stop on its own (state=$state)" >&2
    cat "$STATE_DIR/self-exit-status.json" >&2
    exit 1
  fi
  sleep 2
done

# Companions poll the runtime state every 2s; give them a grace window.
deadline="$((SECONDS + 15))"
while pgrep -f -- "--state-dir $SELF_EXIT_STATE" >/dev/null 2>&1; do
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "companion processes survived guest self-exit:" >&2
    pgrep -af -- "--state-dir $SELF_EXIT_STATE" >&2
    exit 1
  fi
  sleep 1
done

if ! python3 -c 'import socket,sys; s=socket.socket(); s.bind(("127.0.0.1",int(sys.argv[1]))); s.close()' "$PUBLISH_PORT"; then
  echo "published port $PUBLISH_PORT still bound after guest self-exit" >&2
  exit 1
fi

"$CLI" delete self-exit --yes --state-dir "$SELF_EXIT_STATE" >"$STATE_DIR/self-exit-delete.json"

if pgrep -f -- "--state-dir $SELF_EXIT_STATE" >/dev/null 2>&1; then
  echo "companion processes survived delete:" >&2
  pgrep -af -- "--state-dir $SELF_EXIT_STATE" >&2
  exit 1
fi

echo "firecracker user network smoke passed"
