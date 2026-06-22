#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"
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
  e2e_skip "Apple VF network mode smoke requires macOS on Apple silicon"
fi
if [ ! -r "$KERNEL" ]; then
  e2e_skip "kernel is not readable at $KERNEL"
fi
if [ ! -x "$SUPERVISOR" ]; then
  e2e_skip "supervisor is not executable at $SUPERVISOR; run make signed-supervisor"
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

pick_port() {
  python3 - <<'PY'
import socket

with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
}

wait_for_status_ready() {
  local workspace="$1"
  local state_dir="$2"
  local output="$3"
  local deadline="$((SECONDS + 60))"
  while true; do
    "$CLI" status "$workspace" --state-dir "$state_dir" >"$output"
    if python3 - "$output" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    status = json.load(f)
event = status.get("event") or {}
readiness = status.get("readiness") or {}
if event.get("state") == "running" and readiness.get("guestReady", {}).get("ready") and readiness.get("shellReady", {}).get("ready"):
    raise SystemExit(0)
raise SystemExit(1)
PY
    then
      return 0
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "workspace $workspace did not become ready" >&2
      cat "$output" >&2
      return 1
    fi
    sleep 1
  done
}

run_outbound_smoke() {
  local mode="$1"
  local workspace="${mode}-smoke"
  local output="$STATE_DIR/${mode}.json"
  local mode_state="$STATE_DIR/$mode"
  local unsupported_args=()
  if [ "$mode" != "user" ]; then
    unsupported_args=(--unsupported)
  fi
  "$CLI" create "$workspace" \
  --backend apple-vf \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --kernel "$KERNEL" \
  --state-dir "$mode_state" \
  --size-mib "${MICROAGENT_APPLEVF_NETWORK_SIZE_MIB:-128}" \
  --mke2fs "$MKE2FS" \
  --guest-init "$GUEST_INIT" \
  --supervisor "$SUPERVISOR" \
  --memory "${MICROAGENT_APPLEVF_NETWORK_MEMORY_MIB:-512}" \
  --cpus "${MICROAGENT_APPLEVF_NETWORK_CPUS:-2}" \
  --network "$mode" \
  "${unsupported_args[@]+"${unsupported_args[@]}"}" \
  --egress off \
  --service-command "sleep 300" >"$STATE_DIR/${mode}-create.json"
  "$CLI" start "$workspace" \
    --state-dir "$mode_state" \
    --kernel "$KERNEL" \
    --supervisor "$SUPERVISOR" >"$STATE_DIR/${mode}-start.json"
  wait_for_status_ready "$workspace" "$mode_state" "$STATE_DIR/${mode}-status.json"
  "$CLI" connect "$workspace" \
    --state-dir "$mode_state" \
    --send "cat /etc/resolv.conf 2>&1; cat /proc/cmdline 2>&1; cat /proc/net/route 2>&1; ip addr 2>&1 || ifconfig -a 2>&1; wget -qO- -T 10 http://1.1.1.1 >/tmp/applevf-nat.out && echo APPLEVF_NAT_OK" \
    --ready-timeout 30 \
    --timeout "${MICROAGENT_APPLEVF_NETWORK_TIMEOUT_SECONDS:-45}" >"$STATE_DIR/${mode}-connect.txt"
  "$CLI" network "$workspace" --state-dir "$mode_state" >"$output"
  "$CLI" halt "$workspace" --state-dir "$mode_state" --supervisor "$SUPERVISOR" >"$STATE_DIR/${mode}-halt.json"
  "$CLI" delete "$workspace" --yes --state-dir "$mode_state" --supervisor "$SUPERVISOR" >"$STATE_DIR/${mode}-delete.json"

  python3 - "$output" "$STATE_DIR/${mode}-connect.txt" "$STATE_DIR/${mode}-halt.json" "$mode" <<'PY'
import json
import sys

path, connect_path, halt_path, mode = sys.argv[1:]
with open(path, "r", encoding="utf-8") as f:
    network = json.load(f)
with open(connect_path, "r", encoding="utf-8", errors="replace") as f:
    connect = f.read()
with open(halt_path, "r", encoding="utf-8") as f:
    halt = json.load(f)
if "APPLEVF_NAT_OK" not in connect:
    raise SystemExit(connect)
if (network.get("network") or {}).get("mode") != mode:
    raise SystemExit(network)
if halt.get("event", {}).get("state") != "halted":
    raise SystemExit(halt)
PY
}

run_outbound_smoke user
run_outbound_smoke nat

STATIC_WORKSPACE="static-nat-smoke"
STATIC_STATE="$STATE_DIR/static-nat"
cat >"$STATE_DIR/static-nat.yaml" <<YAML
name: $STATIC_WORKSPACE
image: $IMAGE
profile: small
restart: never
resources:
  memoryMiB: ${MICROAGENT_APPLEVF_NETWORK_MEMORY_MIB:-512}
  cpuCount: ${MICROAGENT_APPLEVF_NETWORK_CPUS:-2}
  sizeMiB: ${MICROAGENT_APPLEVF_NETWORK_SIZE_MIB:-128}
network:
  mode: nat
  ip: 192.168.64.2/24
  subnet: 192.168.64.0/24
  gateway: 192.168.64.1
  dns:
    - 1.1.1.1
    - 8.8.8.8
  routes:
    - 0.0.0.0/0 via 192.168.64.1
service: sleep 300
YAML
"$CLI" create \
  --file "$STATE_DIR/static-nat.yaml" \
  --backend apple-vf \
  --arch "$ARCH" \
  --kernel "$KERNEL" \
  --state-dir "$STATIC_STATE" \
  --mke2fs "$MKE2FS" \
  --guest-init "$GUEST_INIT" \
  --supervisor "$SUPERVISOR" \
  --unsupported \
  --egress off >"$STATE_DIR/static-nat-create.json"
"$CLI" start "$STATIC_WORKSPACE" \
  --state-dir "$STATIC_STATE" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" >"$STATE_DIR/static-nat-start.json"
wait_for_status_ready "$STATIC_WORKSPACE" "$STATIC_STATE" "$STATE_DIR/static-nat-status.json"
"$CLI" network "$STATIC_WORKSPACE" --state-dir "$STATIC_STATE" >"$STATE_DIR/static-nat-network.json"
"$CLI" connect "$STATIC_WORKSPACE" \
  --state-dir "$STATIC_STATE" \
  --send "cat /etc/resolv.conf 2>&1; cat /proc/cmdline 2>&1; cat /proc/net/route 2>&1; ip addr 2>&1 || ifconfig -a 2>&1; grep -q '192.168.64.2' /proc/net/fib_trie; grep -q 'nameserver 1.1.1.1' /etc/resolv.conf; wget -qO- -T 10 http://1.1.1.1 >/tmp/applevf-static-nat.out && echo APPLEVF_STATIC_NAT_OK; sync" \
  --ready-timeout 30 \
  --timeout "${MICROAGENT_APPLEVF_NETWORK_TIMEOUT_SECONDS:-45}" >"$STATE_DIR/static-nat-connect.txt"
"$CLI" halt "$STATIC_WORKSPACE" --state-dir "$STATIC_STATE" --supervisor "$SUPERVISOR" >"$STATE_DIR/static-nat-halt.json"
"$CLI" delete "$STATIC_WORKSPACE" --yes --state-dir "$STATIC_STATE" --supervisor "$SUPERVISOR" >"$STATE_DIR/static-nat-delete.json"

python3 - "$STATE_DIR/static-nat-create.json" "$STATE_DIR/static-nat-network.json" "$STATE_DIR/static-nat-connect.txt" <<'PY'
import json
import sys

create_path, network_path, connect_path = sys.argv[1:4]
with open(create_path, "r", encoding="utf-8") as f:
    create = json.load(f)
with open(network_path, "r", encoding="utf-8") as f:
    network = json.load(f)
with open(connect_path, "r", encoding="utf-8", errors="replace") as f:
    connect = f.read()
for body in (create, network):
    cfg = body.get("network") or {}
    if cfg.get("mode") != "nat" or cfg.get("ip") != "192.168.64.2/24" or cfg.get("gateway") != "192.168.64.1":
        raise SystemExit(body)
    if cfg.get("subnet") != "192.168.64.0/24" or cfg.get("dns") != ["1.1.1.1", "8.8.8.8"]:
        raise SystemExit(body)
    if cfg.get("routes") != ["0.0.0.0/0 via 192.168.64.1"]:
        raise SystemExit(body)
if "APPLEVF_STATIC_NAT_OK" not in connect:
    raise SystemExit(connect)
PY

NAMED_STATE="$STATE_DIR/named"
"$CLI" network create devnet --state-dir "$NAMED_STATE" >"$STATE_DIR/named-create-network.txt"
for workspace in db web; do
  "$CLI" create "$workspace" \
    --backend apple-vf \
    --image "$IMAGE" \
    --arch "$ARCH" \
    --kernel "$KERNEL" \
    --state-dir "$NAMED_STATE" \
    --size-mib "${MICROAGENT_APPLEVF_NETWORK_SIZE_MIB:-128}" \
    --mke2fs "$MKE2FS" \
    --guest-init "$GUEST_INIT" \
    --supervisor "$SUPERVISOR" \
    --memory "${MICROAGENT_APPLEVF_NETWORK_MEMORY_MIB:-512}" \
    --cpus "${MICROAGENT_APPLEVF_NETWORK_CPUS:-2}" \
    --network-name devnet \
    --unsupported \
    --egress off \
    --service-command "sleep 300" >"$STATE_DIR/named-${workspace}-create.json"
done
"$CLI" start db \
  --state-dir "$NAMED_STATE" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" >"$STATE_DIR/named-db-start.json"
wait_for_status_ready db "$NAMED_STATE" "$STATE_DIR/named-db-status.json"
"$CLI" start web \
  --state-dir "$NAMED_STATE" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" >"$STATE_DIR/named-web-start.json"
wait_for_status_ready web "$NAMED_STATE" "$STATE_DIR/named-web-status.json"
"$CLI" connect web \
  --state-dir "$NAMED_STATE" \
  --send "cat /proc/cmdline 2>&1; cat /etc/hosts 2>&1; ping -c 1 -W 5 db >/tmp/applevf-named-ping.out && echo APPLEVF_NAMED_OK; sync" \
  --ready-timeout 30 \
  --timeout "${MICROAGENT_APPLEVF_NETWORK_TIMEOUT_SECONDS:-45}" >"$STATE_DIR/named-web-connect.txt"
"$CLI" network web --state-dir "$NAMED_STATE" >"$STATE_DIR/named-web-network.json"
"$CLI" halt web --state-dir "$NAMED_STATE" --supervisor "$SUPERVISOR" >"$STATE_DIR/named-web-halt.json"
"$CLI" halt db --state-dir "$NAMED_STATE" --supervisor "$SUPERVISOR" >"$STATE_DIR/named-db-halt.json"
"$CLI" delete web --yes --state-dir "$NAMED_STATE" --supervisor "$SUPERVISOR" >"$STATE_DIR/named-web-delete.json"
"$CLI" delete db --yes --state-dir "$NAMED_STATE" --supervisor "$SUPERVISOR" >"$STATE_DIR/named-db-delete.json"

python3 - "$STATE_DIR/named-web-network.json" "$STATE_DIR/named-web-connect.txt" <<'PY'
import json
import sys

network_path, connect_path = sys.argv[1:3]
with open(network_path, "r", encoding="utf-8") as f:
    network = json.load(f)
with open(connect_path, "r", encoding="utf-8", errors="replace") as f:
    connect = f.read()
runtime = network.get("runtime") or {}
if runtime.get("mode") != "named" or runtime.get("name") != "devnet":
    raise SystemExit(network)
if runtime.get("ip") != "10.44.1.3/24" or runtime.get("gateway") != "10.44.1.1":
    raise SystemExit(network)
hosts = runtime.get("hosts") or []
if "db:10.44.1.2" not in hosts or "web:10.44.1.3" not in hosts:
    raise SystemExit(network)
if "APPLEVF_NAMED_OK" not in connect:
    raise SystemExit(connect)
PY

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

duplicate_port="$(pick_port)"
if "$CLI" create publish-collision \
  --backend apple-vf \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --kernel "$KERNEL" \
  --state-dir "$STATE_DIR/publish-collision" \
  --size-mib "${MICROAGENT_APPLEVF_NETWORK_SIZE_MIB:-128}" \
  --mke2fs "$MKE2FS" \
  --guest-init "$GUEST_INIT" \
  --supervisor "$SUPERVISOR" \
  --network user \
  --publish "127.0.0.1:$duplicate_port:4222/tcp" \
  --publish "127.0.0.1:$duplicate_port:8222/tcp" >"$STATE_DIR/publish-collision.json" 2>"$STATE_DIR/publish-collision.err"; then
  echo "duplicate published host port unexpectedly succeeded" >&2
  exit 1
fi
grep -qi "duplicate published host port" "$STATE_DIR/publish-collision.err"

if "$CLI" create publish-udp \
  --backend apple-vf \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --kernel "$KERNEL" \
  --state-dir "$STATE_DIR/publish-udp" \
  --size-mib "${MICROAGENT_APPLEVF_NETWORK_SIZE_MIB:-128}" \
  --mke2fs "$MKE2FS" \
  --guest-init "$GUEST_INIT" \
  --supervisor "$SUPERVISOR" \
  --network user \
  --publish "127.0.0.1:$(pick_port):8222/udp" >"$STATE_DIR/publish-udp.json" 2>"$STATE_DIR/publish-udp.err"; then
  echo "udp published port unexpectedly succeeded" >&2
  exit 1
fi
grep -qi "protocol must be tcp" "$STATE_DIR/publish-udp.err"

if "$CLI" create publish-ipv6 \
  --backend apple-vf \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --kernel "$KERNEL" \
  --state-dir "$STATE_DIR/publish-ipv6" \
  --size-mib "${MICROAGENT_APPLEVF_NETWORK_SIZE_MIB:-128}" \
  --mke2fs "$MKE2FS" \
  --guest-init "$GUEST_INIT" \
  --supervisor "$SUPERVISOR" \
  --network user \
  --publish "[::1]:$(pick_port):8222/tcp" >"$STATE_DIR/publish-ipv6.json" 2>"$STATE_DIR/publish-ipv6.err"; then
  echo "ipv6 published port unexpectedly succeeded" >&2
  exit 1
fi
grep -qi "publish mapping must be" "$STATE_DIR/publish-ipv6.err"

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
