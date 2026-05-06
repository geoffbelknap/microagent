#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SUPERVISOR="${MICROAGENT_APPLEVF_SUPERVISOR:-$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor}"
KERNEL="${MICROAGENT_APPLEVF_KERNEL:-$HOME/.microagent/kernels/apple-vf/arm64/Image}"
if [ ! -r "$KERNEL" ] && [ -r "$HOME/.microagent/kernels/apple-vf/Image" ]; then
  KERNEL="$HOME/.microagent/kernels/apple-vf/Image"
fi
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-applevf-network.XXXXXX")"
ROOTFS="$STATE_DIR/rootfs.ext4"

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

NAT_RESPONSE="$(run_check nat-check nat)"
python3 - "$NAT_RESPONSE" <<'PY'
import json
import sys
resp = json.loads(sys.argv[1])
if not resp.get("ok"):
    raise SystemExit(resp)
PY

ISOLATED_RESPONSE="$(run_check isolated-check isolated)"
python3 - "$ISOLATED_RESPONSE" <<'PY'
import json
import sys
resp = json.loads(sys.argv[1])
if not resp.get("ok"):
    raise SystemExit(resp)
PY

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

PUBLISH_ERROR="$(python3 - "$KERNEL" "$ROOTFS" "$STATE_DIR" <<'PY' | "$SUPERVISOR" || true
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

python3 - "$PUBLISH_ERROR" <<'PY'
import json
import sys
resp = json.loads(sys.argv[1])
err = resp.get("error", "")
if "Apple VF network.portForwards are not implemented" not in err:
    raise SystemExit(f"unexpected publish error: {err}")
PY

echo "Apple VF network mode smoke passed; bridged $BRIDGE_RESULT"
