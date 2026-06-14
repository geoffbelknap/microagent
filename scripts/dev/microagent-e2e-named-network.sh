#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

# User-defined named networks: two workspaces join one network, get stable IPs
# from its subnet, share a managed bridge, reach each other by IP, and resolve
# each other by name via /etc/hosts. Privileged: needs root/CAP_NET_ADMIN and
# net.ipv4.ip_forward=1 (Firecracker/Linux only).
e2e_require_netpriv
e2e_require_cmd mke2fs "mke2fs is required to build the workspace rootfs"

STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-named-network.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox:1.36}"
NET="e2e-devnet"
SUBNET="10.44.71.0/24"
BRIDGE="mbr$(printf '%s' "$NET" | sha1sum | cut -c1-8)"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    for ws in web db; do
      "$CLI" kill "$ws" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
      "$CLI" delete "$ws" --force --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    done
  fi
  ip link del "$BRIDGE" >/dev/null 2>&1 || true
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_NAMED_NETWORK:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E named-network state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

# Enable IPv4 forwarding if we can (root); required for named/nat egress.
if [ "$(cat /proc/sys/net/ipv4/ip_forward 2>/dev/null || echo 0)" != "1" ]; then
  if e2e_is_root; then
    sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1 || true
  fi
fi

cd "$ROOT"
export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
e2e_build_firecracker_stack "$CLI" "$SUPERVISOR" "$GUEST_INIT"
"$CLI" kernel install --backend firecracker --arch amd64 >"$STATE_DIR/kernel-install.json" 2>/dev/null || e2e_fail "kernel install"
kernel_path="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["path"])' "$STATE_DIR/kernel-install.json")"

run_cli() { "$CLI" "$@" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR"; }
# network and exec do not take --supervisor (registry op / direct guest connect).
net_cli() { "$CLI" "$@" --state-dir "$STATE_DIR"; }
exec_ws() { ws="$1"; shift; "$CLI" exec "$ws" --state-dir "$STATE_DIR" "$@"; }

e2e_step "create named network $NET ($SUBNET)"
net_cli network create "$NET" --subnet "$SUBNET" >/dev/null 2>&1 || e2e_fail "network create"

for ws in web db; do
  e2e_step "create + start $ws on $NET"
  run_cli create "$ws" --image "$IMAGE" --network-name "$NET" --service-command "sleep 600" \
    --kernel "$kernel_path" --guest-init "$GUEST_INIT" --size-mib 128 --result-port 0 >/dev/null 2>&1 || e2e_fail "create $ws"
  run_cli start "$ws" >/dev/null 2>&1 || e2e_fail "start $ws"
  e2e_wait_exec_ready "$CLI" "$STATE_DIR" "$ws" || e2e_fail "exec service never became ready for $ws"
done

DB_IP="$(python3 -c "import json;d=json.load(open('$STATE_DIR/networks/index.json'));print([m['ip'] for n in d['networks'] if n['name']=='$NET' for m in n['members'] if m['workspace']=='db'][0])")"
WEB_IP="$(python3 -c "import json;d=json.load(open('$STATE_DIR/networks/index.json'));print([m['ip'] for n in d['networks'] if n['name']=='$NET' for m in n['members'] if m['workspace']=='web'][0])")"
e2e_log "allocated web=$WEB_IP db=$DB_IP on bridge $BRIDGE"
if [ -z "$DB_IP" ] || [ -z "$WEB_IP" ]; then
  e2e_fail "members did not receive stable IPs"
fi

e2e_step "cross-VM connectivity by IP (web -> db)"
exec_ws web -- ping -c2 -w5 "$DB_IP" >"$STATE_DIR/ping-ip.txt" 2>&1 || { cat "$STATE_DIR/ping-ip.txt"; e2e_fail "ping by IP"; }
grep -q "2 packets received\|2 received" "$STATE_DIR/ping-ip.txt" || e2e_fail "ping by IP lost packets"

e2e_step "name resolution (db booted last knows web; ping web by name)"
exec_ws db -- cat /etc/hosts >"$STATE_DIR/db-hosts.txt" 2>&1 || e2e_fail "read db hosts"
grep -q "$WEB_IP" "$STATE_DIR/db-hosts.txt" || e2e_fail "web not in db /etc/hosts"
exec_ws db -- ping -c2 -w5 web >"$STATE_DIR/ping-name.txt" 2>&1 || { cat "$STATE_DIR/ping-name.txt"; e2e_fail "ping by name"; }
grep -q "2 packets received\|2 received" "$STATE_DIR/ping-name.txt" || e2e_fail "ping by name lost packets"

e2e_step "restart refreshes the earlier member's /etc/hosts"
run_cli stop web >/dev/null 2>&1 || true
run_cli start web >/dev/null 2>&1 || e2e_fail "restart web"
e2e_wait_exec_ready "$CLI" "$STATE_DIR" web || e2e_fail "exec service never became ready after restart"
exec_ws web -- ping -c2 -w5 db >"$STATE_DIR/ping-name2.txt" 2>&1 || { cat "$STATE_DIR/ping-name2.txt"; e2e_fail "ping db by name after restart"; }
grep -q "2 packets received\|2 received" "$STATE_DIR/ping-name2.txt" || e2e_fail "post-restart ping by name lost packets"

e2e_step "cleanup releases addresses and reaps the bridge"
run_cli delete web --yes --force >/dev/null 2>&1 || true
run_cli delete db --yes --force >/dev/null 2>&1 || true
if ip link show "$BRIDGE" >/dev/null 2>&1; then
  e2e_fail "managed bridge $BRIDGE not reaped after last member left"
fi
net_cli network delete "$NET" --force >/dev/null 2>&1 || true

e2e_log "named-network scenario passed"
