#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-firecracker-network.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
IMAGE="docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f"
EXPECTED_KERNEL_SHA="4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0"

cleanup() {
  status="$?"
  if [ "$status" -eq 0 ] && [ -x "$CLI" ]; then
    "$CLI" stop isolated-smoke --state-dir "$STATE_DIR/isolated" >/dev/null 2>&1 || true
    "$CLI" delete isolated-smoke --state-dir "$STATE_DIR/isolated" >/dev/null 2>&1 || true
    "$CLI" stop bridged-smoke --state-dir "$STATE_DIR/bridged" >/dev/null 2>&1 || true
    "$CLI" delete bridged-smoke --state-dir "$STATE_DIR/bridged" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_FIRECRACKER_NETWORK_SMOKE:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept firecracker network smoke state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    echo "firecracker network mode smoke requires Linux amd64" >&2
    exit 2
    ;;
esac

if [ ! -e /dev/kvm ]; then
  echo "/dev/kvm is not visible; run this smoke outside sandboxed environments" >&2
  exit 2
fi

if [ -n "${MICROAGENT_FIRECRACKER:-}" ]; then
  firecracker="$MICROAGENT_FIRECRACKER"
elif command -v firecracker >/dev/null 2>&1; then
  firecracker="$(command -v firecracker)"
elif command -v brew >/dev/null 2>&1; then
  formula_prefix="$(brew --prefix microagent-kit 2>/dev/null || true)"
  firecracker="$formula_prefix/libexec/firecracker"
else
  firecracker=""
fi

if [ ! -x "${firecracker:-}" ]; then
  echo "firecracker binary not found; install microagent-kit or set MICROAGENT_FIRECRACKER" >&2
  exit 2
fi

host_port="$(python3 - <<'PY'
import socket

with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
    s.bind(("127.0.0.1", 0))
    print(s.getsockname()[1])
PY
)"

export GOCACHE="$STATE_DIR/gocache"
export GOMODCACHE="$STATE_DIR/gomodcache"
export GOFLAGS="${GOFLAGS:-} -modcacherw"
export MICROAGENT_FIRECRACKER="$firecracker"
export MICROAGENT_FIRECRACKER_SUPERVISOR="$SUPERVISOR"

(
  cd "$ROOT"
  go build -o "$CLI" ./cmd/microagent
  go build -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

if [ "$(id -u)" -ne 0 ]; then
  if ! command -v getcap >/dev/null 2>&1; then
    echo "getcap is required to assert supervisor-only CAP_NET_ADMIN setup" >&2
    exit 2
  fi
  supervisor_caps="$(getcap "$SUPERVISOR" 2>/dev/null || true)"
  if ! printf '%s\n' "$supervisor_caps" | grep -q 'cap_net_admin=eip'; then
    if command -v sudo >/dev/null 2>&1; then
      sudo -n setcap 'cap_net_admin+eip' "$SUPERVISOR" 2>/dev/null || true
      supervisor_caps="$(getcap "$SUPERVISOR" 2>/dev/null || true)"
    fi
  fi
  if ! printf '%s\n' "$supervisor_caps" | grep -q 'cap_net_admin=eip'; then
    echo "grant only the supervisor CAP_NET_ADMIN before running this smoke:" >&2
    echo "  sudo setcap 'cap_net_admin+eip' $SUPERVISOR" >&2
    exit 2
  fi
  for host_tool in ip iptables xtables-nft-multi; do
    if tool_path="$(command -v "$host_tool" 2>/dev/null)"; then
      tool_caps="$(getcap "$tool_path" 2>/dev/null || true)"
      if printf '%s\n' "$tool_caps" | grep -q 'cap_net_admin'; then
        echo "$host_tool has CAP_NET_ADMIN; remove that capability to verify supervisor-only networking privileges" >&2
        exit 2
      fi
    fi
  done
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

if [ "$(id -u)" -ne 0 ] && command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
  sudo -n setcap 'cap_net_admin+ep' "$SUPERVISOR"
  "$CLI" run \
    --backend firecracker \
    --image "$IMAGE" \
    --arch amd64 \
    --exec "echo SHOULD_NOT_BOOT" \
    --kernel "$kernel_path" \
    --guest-init "$GUEST_INIT" \
    --state-dir "$STATE_DIR/nat-missing-inheritable" \
    --size-mib 128 \
    --result-port 0 \
    --timeout 30 \
    --network nat >"$STATE_DIR/nat-missing-inheritable.json" 2>"$STATE_DIR/nat-missing-inheritable.err" || true
  if ! grep -q 'cap_net_admin+eip' "$STATE_DIR/nat-missing-inheritable.json" "$STATE_DIR/nat-missing-inheritable.err"; then
    echo "nat with cap_net_admin+ep did not fail with the inheritable capability error" >&2
    cat "$STATE_DIR/nat-missing-inheritable.json" >&2
    cat "$STATE_DIR/nat-missing-inheritable.err" >&2
    exit 1
  fi
  if grep -q 'SHOULD_NOT_BOOT' "$STATE_DIR/nat-missing-inheritable.json" "$STATE_DIR/nat-missing-inheritable.err"; then
    echo "nat with cap_net_admin+ep unexpectedly booted" >&2
    exit 1
  fi
  sudo -n setcap 'cap_net_admin+eip' "$SUPERVISOR"
fi

"$CLI" run \
  --backend firecracker \
  --image "$IMAGE" \
  --arch amd64 \
  --exec "wget -qO- -T 10 http://example.com >/tmp/nat.out && echo NAT_OUTBOUND_READY || echo NAT_OUTBOUND_FAILED" \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR/nat-run" \
  --size-mib 128 \
  --result-port 0 \
  --timeout 30 \
  --network nat \
  --keep >"$STATE_DIR/nat.json"

python3 - "$STATE_DIR/nat.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)
if result["response"]["event"]["state"] != "stopped":
    raise SystemExit(result)
if "NAT_OUTBOUND_FAILED" in result["serial_log"]:
    raise SystemExit(result["serial_log"])
if "NAT_OUTBOUND_READY" not in result["serial_log"]:
    raise SystemExit(result["serial_log"])
if result["network"]["mode"] != "nat":
    raise SystemExit(result["network"])
runtime = ((result.get("response") or {}).get("network") or {})
if runtime.get("mode") == "nat" and runtime.get("ip") == "":
    raise SystemExit(runtime)
PY

if "$CLI" create isolated-publish \
  --image "$IMAGE" \
  --arch amd64 \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR/isolated-publish" \
  --size-mib 128 \
  --result-port 0 \
  --network isolated \
  --publish "127.0.0.1:${host_port}:8080/tcp" >"$STATE_DIR/isolated-publish.json" 2>"$STATE_DIR/isolated-publish.err"; then
  echo "isolated publish unexpectedly succeeded" >&2
  exit 1
fi
grep -q "network.portForwards require nat or bridged mode" "$STATE_DIR/isolated-publish.err"

"$CLI" create isolated-smoke \
  --image "$IMAGE" \
  --arch amd64 \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR/isolated" \
  --size-mib 128 \
  --result-port 0 \
  --network isolated >"$STATE_DIR/isolated-create.json"

"$CLI" start isolated-smoke --state-dir "$STATE_DIR/isolated" --kernel "$kernel_path" >"$STATE_DIR/isolated-start.json"
"$CLI" connect isolated-smoke \
  --state-dir "$STATE_DIR/isolated" \
  --send "test ! -e /sys/class/net/eth0 && echo ISOLATED_READY" \
  --timeout 2 >"$STATE_DIR/isolated-connect.txt"
"$CLI" network isolated-smoke --state-dir "$STATE_DIR/isolated" >"$STATE_DIR/isolated-network.json"
"$CLI" status isolated-smoke --state-dir "$STATE_DIR/isolated" >"$STATE_DIR/isolated-status.json"
"$CLI" ps --state-dir "$STATE_DIR/isolated" >"$STATE_DIR/isolated-ps.json"
"$CLI" stop isolated-smoke --state-dir "$STATE_DIR/isolated" >"$STATE_DIR/isolated-stop.json"
"$CLI" delete isolated-smoke --state-dir "$STATE_DIR/isolated" >"$STATE_DIR/isolated-delete.json"

python3 - "$STATE_DIR/isolated-connect.txt" "$STATE_DIR/isolated-network.json" "$STATE_DIR/isolated-status.json" "$STATE_DIR/isolated-ps.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8", errors="replace") as f:
    connect = f.read()
with open(sys.argv[2], "r", encoding="utf-8") as f:
    network = json.load(f)
with open(sys.argv[3], "r", encoding="utf-8") as f:
    status = json.load(f)
with open(sys.argv[4], "r", encoding="utf-8") as f:
    ps = json.load(f)
if "ISOLATED_READY" not in connect:
    raise SystemExit(connect)
if network["network"]["mode"] != "isolated":
    raise SystemExit(network)
if status["network"]["mode"] != "isolated":
    raise SystemExit(status)
if not any(entry["name"] == "isolated-smoke" and entry["network"] == "isolated" for entry in ps["workspaces"]):
    raise SystemExit(ps)
PY

"$CLI" create bridged-missing-interface \
  --image "$IMAGE" \
  --arch amd64 \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR/bridged-missing" \
  --size-mib 128 \
  --result-port 0 \
  --network bridged >"$STATE_DIR/bridged-missing.json" 2>"$STATE_DIR/bridged-missing.err"
python3 - "$STATE_DIR/bridged-missing.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)
err = result.get("response", {}).get("error", "")
if result.get("response", {}).get("ok") is not False:
    raise SystemExit(result)
if "firecracker network.interface is required for bridged mode" not in err:
    raise SystemExit(result)
PY

BRIDGE_RESULT="host-prerequisite-not-configured"
if [ -n "${MICROAGENT_FIRECRACKER_BRIDGE_INTERFACE:-}" ]; then
  bridge="$MICROAGENT_FIRECRACKER_BRIDGE_INTERFACE"
  "$CLI" run \
    --backend firecracker \
    --image "$IMAGE" \
    --arch amd64 \
    --exec "echo BRIDGED_READY" \
    --kernel "$kernel_path" \
    --guest-init "$GUEST_INIT" \
    --state-dir "$STATE_DIR/bridged" \
    --size-mib 128 \
    --result-port 0 \
    --timeout 30 \
    --network bridged \
    --network-interface "$bridge" \
    --keep >"$STATE_DIR/bridged.json"
  python3 - "$STATE_DIR/bridged.json" "$bridge" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)
if result["response"]["event"]["state"] != "stopped":
    raise SystemExit(result)
if "BRIDGED_READY" not in result["serial_log"]:
    raise SystemExit(result["serial_log"])
if result["network"]["mode"] != "bridged" or result["network"].get("interface") != sys.argv[2]:
    raise SystemExit(result["network"])
PY
  BRIDGE_RESULT="booted-on-$bridge"
else
  "$CLI" create bridged-nonbridge \
    --image "$IMAGE" \
    --arch amd64 \
    --kernel "$kernel_path" \
    --guest-init "$GUEST_INIT" \
    --state-dir "$STATE_DIR/bridged-nonbridge" \
    --size-mib 128 \
    --result-port 0 \
    --network bridged \
    --network-interface lo >"$STATE_DIR/bridged-nonbridge.json" 2>"$STATE_DIR/bridged-nonbridge.err"
  python3 - "$STATE_DIR/bridged-nonbridge.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)
err = result.get("response", {}).get("error", "")
if result.get("response", {}).get("ok") is not False:
    raise SystemExit(result)
if 'firecracker bridged network.interface "lo" must be a Linux bridge' not in err:
    raise SystemExit(result)
PY
fi

echo "firecracker network mode smoke passed; bridged $BRIDGE_RESULT"
