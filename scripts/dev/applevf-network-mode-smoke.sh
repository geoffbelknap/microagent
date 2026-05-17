#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SUPERVISOR="${MICROAGENT_APPLEVF_SUPERVISOR:-$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor}"
KERNEL="${MICROAGENT_APPLEVF_KERNEL:-$HOME/.microagent/kernels/apple-vf/arm64/Image}"
if [ ! -r "$KERNEL" ] && [ -r "$HOME/.microagent/kernels/apple-vf/Image" ]; then
  KERNEL="$HOME/.microagent/kernels/apple-vf/Image"
fi
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-applevf-network.XXXXXX")"
ROOTFS="$STATE_DIR/rootfs.ext4"
CLI="$STATE_DIR/microagent"
GUEST_INIT="$STATE_DIR/microagent-guestinit"
IMAGE="${MICROAGENT_APPLEVF_NETWORK_IMAGE:-docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6}"
ARCH="${MICROAGENT_APPLEVF_NETWORK_ARCH:-arm64}"

cleanup() {
  if [ "${MICROAGENT_KEEP_NETWORK_SMOKE:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept network smoke state at $STATE_DIR"
  fi
}
trap cleanup EXIT

if [ "$(uname -s)" != "Darwin" ] || [ "$(uname -m)" != "arm64" ]; then
  echo "Apple VF network mode smoke requires macOS on Apple silicon" >&2
  exit 2
fi
if [ ! -r "$KERNEL" ]; then
  echo "kernel is not readable at $KERNEL" >&2
  exit 2
fi
if [ ! -x "$SUPERVISOR" ]; then
  echo "supervisor is not executable at $SUPERVISOR; run make signed-supervisor" >&2
  exit 2
fi
if command -v mke2fs >/dev/null 2>&1; then
  MKE2FS="$(command -v mke2fs)"
elif [ -x /opt/homebrew/opt/e2fsprogs/sbin/mke2fs ]; then
  MKE2FS="/opt/homebrew/opt/e2fsprogs/sbin/mke2fs"
else
  echo "mke2fs not found; install e2fsprogs" >&2
  exit 2
fi

(
  cd "$ROOT"
  go build -o "$CLI" ./cmd/microagent
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

: >"$ROOTFS"

request() {
  local runtime="$1"
  local mode="$2"
  local iface="${3:-}"
  python3 - "$runtime" "$mode" "$iface" "$KERNEL" "$ROOTFS" "$STATE_DIR" <<'PY'
import json
import sys

runtime, mode, iface, kernel, rootfs, state = sys.argv[1:]
network = {"mode": mode}
if iface:
    network["interface"] = iface
req = {
    "command": "check",
    "identity": {
        "requestID": f"req-{runtime}",
        "runtimeID": runtime,
        "role": "workload",
        "backend": "apple-vf",
    },
    "config": {
        "kernelPath": kernel,
        "rootfsPath": rootfs,
        "stateDir": state,
        "memoryMiB": 512,
        "cpuCount": 2,
        "network": network,
    },
}
print(json.dumps(req))
PY
}

run_check() {
  local runtime="$1"
  local mode="$2"
  local iface="${3:-}"
  request "$runtime" "$mode" "$iface" | "$SUPERVISOR"
}

run_outbound_smoke() {
  local mode="$1"
  local output="$STATE_DIR/${mode}.json"
  "$CLI" run \
  --backend apple-vf \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --exec "wget -qO- -T 10 http://example.com >/tmp/applevf-nat.out && echo APPLEVF_NAT_OK" \
  --name "${mode}-smoke" \
  --kernel "$KERNEL" \
  --state-dir "$STATE_DIR/$mode" \
  --size-mib "${MICROAGENT_APPLEVF_NETWORK_SIZE_MIB:-128}" \
  --mke2fs "$MKE2FS" \
  --guest-init "$GUEST_INIT" \
  --supervisor "$SUPERVISOR" \
  --memory "${MICROAGENT_APPLEVF_NETWORK_MEMORY_MIB:-512}" \
  --cpus "${MICROAGENT_APPLEVF_NETWORK_CPUS:-2}" \
  --network "$mode" \
  --timeout "${MICROAGENT_APPLEVF_NETWORK_TIMEOUT_SECONDS:-45}" >"$output"

  python3 - "$output" "$mode" <<'PY'
import json
import sys

path, mode = sys.argv[1:]
with open(path, "r", encoding="utf-8") as f:
    result = json.load(f)
response = result.get("response") or {}
if (response.get("event") or {}).get("state") != "stopped":
    raise SystemExit(result)
guest = result.get("result") or {}
stdout = guest.get("stdout", "")
stderr = guest.get("stderr", "")
serial = result.get("serial_log", "")
if "APPLEVF_NAT_OK" not in stdout and "APPLEVF_NAT_OK" not in serial:
    raise SystemExit(result)
if "bad address" in stderr.lower() or "network is unreachable" in stderr.lower():
    raise SystemExit(result)
if guest.get("exit_code") != 0:
    raise SystemExit(result)
if (result.get("network") or {}).get("mode") != mode:
    raise SystemExit(result.get("network"))
PY
}

run_outbound_smoke user
run_outbound_smoke nat

ISOLATED_RESPONSE="$(run_check isolated-check isolated)"
python3 - "$ISOLATED_RESPONSE" <<'PY'
import json
import sys
resp = json.loads(sys.argv[1])
if not resp.get("ok"):
    raise SystemExit(resp)
PY

if "$CLI" run \
  --backend apple-vf \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --exec "true" \
  --name isolated-publish-smoke \
  --kernel "$KERNEL" \
  --state-dir "$STATE_DIR/isolated-publish" \
  --size-mib "${MICROAGENT_APPLEVF_NETWORK_SIZE_MIB:-128}" \
  --mke2fs "$MKE2FS" \
  --guest-init "$GUEST_INIT" \
  --supervisor "$SUPERVISOR" \
  --network isolated \
  --publish "127.0.0.1:8080:80/tcp" >"$STATE_DIR/isolated-publish.json" 2>"$STATE_DIR/isolated-publish.err"; then
  echo "Apple VF isolated publish was accepted unexpectedly" >&2
  exit 1
fi
grep -q "network.portForwards require user, nat, or bridged mode" "$STATE_DIR/isolated-publish.err"

BRIDGE_ERROR="$(run_check bridged-missing-interface bridged || true)"
BRIDGE_STATUS="$(python3 - "$BRIDGE_ERROR" <<'PY'
import json
import re
import sys
try:
    resp = json.loads(sys.argv[1])
except json.JSONDecodeError as exc:
    raise SystemExit(f"invalid supervisor response: {exc}")
err = resp.get("error", "")
if "com.apple.vm.networking" in err:
    print("entitlement-gated")
    raise SystemExit(0)
if "network.interface is required for bridged mode" not in err:
    raise SystemExit(f"unexpected bridged error: {err}")
match = re.search(r"available interfaces: ([^,(]+)", err)
if not match:
    raise SystemExit(f"could not find a bridged interface in: {err}")
print("interface=" + match.group(1).strip())
PY
)"

if [ "$BRIDGE_STATUS" = "entitlement-gated" ]; then
  BRIDGE_RESULT="entitlement-gated"
else
  BRIDGE_IFACE="${BRIDGE_STATUS#interface=}"
  BRIDGED_RESPONSE="$(run_check bridged-check bridged "$BRIDGE_IFACE")"
  python3 - "$BRIDGED_RESPONSE" <<'PY'
import json
import sys
resp = json.loads(sys.argv[1])
if not resp.get("ok"):
    raise SystemExit(resp)
PY
  BRIDGE_RESULT="interface $BRIDGE_IFACE"
fi

PUBLISH_RESPONSE="$(python3 - "$KERNEL" "$ROOTFS" "$STATE_DIR" <<'PY' | "$SUPERVISOR"
import json
import sys
kernel, rootfs, state = sys.argv[1:]
print(json.dumps({
    "command": "check",
    "identity": {
        "requestID": "req-publish",
        "runtimeID": "publish-check",
        "role": "workload",
        "backend": "apple-vf",
    },
    "config": {
        "kernelPath": kernel,
        "rootfsPath": rootfs,
        "stateDir": state,
        "network": {
            "mode": "nat",
            "portForwards": [{"protocol": "tcp", "host": "127.0.0.1", "hostPort": 8080, "guestPort": 80}],
        },
    },
}))
PY
)"

python3 - "$PUBLISH_RESPONSE" <<'PY'
import json
import sys
resp = json.loads(sys.argv[1])
if not resp.get("ok"):
    raise SystemExit(resp)
PY

echo "Apple VF network mode smoke passed; bridged $BRIDGE_RESULT"
