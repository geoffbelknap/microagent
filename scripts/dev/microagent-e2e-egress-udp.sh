#!/usr/bin/env bash
# microagent E2E: mediated-default transparent egress with UDP + DNS mediation
# (rootless user/pasta mode).
#
# Proves the complete-mediation default (`egress_mode: mediated`): the mediator
# captures BOTH guest TCP (in-netns nftables REDIRECT -> MITM) AND guest UDP
# (in-netns nftables TPROXY -> transparent socket), is the guest's authoritative
# resolver, and — being mediated, not strict — allows everything while auditing:
#   - UDP plane: the mediator opens a transparent UDP socket (egress_udp_listen)
#     and guest DNS datagrams are TPROXY-steered to it, forwarded, and answered
#     (egress_dns_allow, unlisted=true because mediated permits names on no
#     allowlist). This is the only way DNS resolves, so its presence proves the
#     TPROXY/UDP capture path end-to-end.
#   - TCP plane: the captured HTTP connection is forwarded (egress_allow), which
#     proves the REDIRECT/TCP capture path end-to-end.
#   - mediated allows ALL hosts: two unrelated hosts both fetch successfully,
#     regardless of any allowlist (none is needed).
#   - the mediator process is torn down with the workspace (no orphan), and the
#     transient REDIRECT + TPROXY nft rules vanish with the ephemeral netns.
#
# Runs entirely in `--network user` (pasta): no host CAP_NET_ADMIN / root needed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh
. "$ROOT/scripts/dev/e2e-lib.sh"
export PATH="/home/linuxbrew/.linuxbrew/bin:/home/linuxbrew/.linuxbrew/sbin:/opt/homebrew/bin:/opt/homebrew/sbin:$PATH"

STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-egress.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
WORKSPACE="egress-mediated-udp"
IMAGE="${MICROAGENT_EGRESS_IMAGE:-docker.io/library/alpine:3.20}"
# mediated allows EVERYTHING; two unrelated hosts prove "allow all" rather than
# an allowlist match. No egress_allow list is configured for this workspace.
HOST_A="example.com"
HOST_B="example.org"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    "$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_E2E_EGRESS:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent egress-udp E2E state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64) ;;
  *) e2e_skip "microagent egress-udp E2E requires Linux amd64" ;;
esac

for required in pasta python3; do
  command -v "$required" >/dev/null 2>&1 || e2e_skip "$required is required for the egress-udp E2E"
done
[ -e /dev/kvm ] || e2e_skip "/dev/kvm is not visible"
[ -e /dev/net/tun ] || e2e_skip "/dev/net/tun is not visible (user networking needs tun)"
if [ -e /proc/sys/user/max_user_namespaces ] && [ "$(cat /proc/sys/user/max_user_namespaces)" = "0" ]; then
  e2e_skip "user.max_user_namespaces is 0"
fi

if [ -n "${MICROAGENT_FIRECRACKER:-}" ]; then
  firecracker="$MICROAGENT_FIRECRACKER"
elif command -v firecracker >/dev/null 2>&1; then
  firecracker="$(command -v firecracker)"
elif command -v brew >/dev/null 2>&1; then
  firecracker="$(brew --prefix microagent 2>/dev/null || true)/libexec/firecracker"
else
  firecracker=""
fi
[ -x "${firecracker:-}" ] || e2e_skip "egress-udp E2E needs the Firecracker backend binary; install firecracker or set MICROAGENT_FIRECRACKER"

export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
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
kernel_path="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["path"])' "$STATE_DIR/kernel-install.json")"

echo "pulling $IMAGE -> rootfs" >&2
"$CLI" image pull "$IMAGE" \
  --state-dir "$STATE_DIR/cache" --arch amd64 \
  --guest-init "$GUEST_INIT" --size-mib 128 >"$STATE_DIR/image-pull.json"
rootfs_src="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["output_path"])' "$STATE_DIR/image-pull.json")"

# Prepare a mediated-default workspace by writing the manifest directly (mirrors
# the strict egress E2E). egress_mode: mediated provisions the mediator and BOTH
# capture rules (REDIRECT for TCP, TPROXY for UDP) with NO allowlist — mediated
# allows all. The CLI flag -> manifest plumbing is unit-tested; this exercises
# the supervisor runtime path that reads egress_mode from the manifest and wires
# the UDP/TPROXY plane.
mkdir -p "$STATE_DIR/workspaces/$WORKSPACE" "$STATE_DIR/$WORKSPACE"
cp "$rootfs_src" "$STATE_DIR/workspaces/$WORKSPACE/rootfs.ext4"
python3 - "$STATE_DIR" "$WORKSPACE" <<'PY'
import json, os, sys, time
state_dir, name = sys.argv[1:3]
now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
manifest = {
    "name": name, "profile": "small", "restart": "never",
    "resources": {"memory_mib": 512, "cpu_count": 2, "size_mib": 128},
    "network": {"mode": "user"},
    "egress_mode": "mediated",
}
event = {
    "identity": {"requestID": name + "-prepared", "runtimeID": name,
                 "role": "workload", "backend": "linux-kvm"},
    "state": "prepared", "detail": "prepared for egress-udp E2E", "observedAt": now,
}
json.dump(manifest, open(os.path.join(state_dir, "workspaces", name, "workspace.json"), "w"), indent=2, sort_keys=True)
json.dump(event, open(os.path.join(state_dir, name, "event.json"), "w"), indent=2, sort_keys=True)
PY

"$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/start.json"
deadline="$((SECONDS + 60))"
while true; do
  state="$("$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" 2>/dev/null | python3 -c 'import sys,json;print((json.load(sys.stdin).get("event") or {}).get("state",""))' 2>/dev/null || true)"
  [ "$state" = "running" ] && break
  if [ "$state" = "failed" ] || [ "$SECONDS" -ge "$deadline" ]; then
    echo "workspace did not reach running (state=$state)" >&2
    "$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >&2 || true
    exit 1
  fi
  sleep 1
done

# Capture the mediator's host pid for the teardown check (bracketed pattern so the
# grep line itself is not matched).
mediator_pid="$(pgrep -af 'egress[-]mediator' | grep "$STATE_DIR" | awk '{print $1}' | head -1 || true)"
[ -n "$mediator_pid" ] || { echo "no egress mediator process found for workspace" >&2; exit 1; }

# Two unrelated hosts: mediated must allow both. Each fetch first resolves the
# name (UDP DNS via TPROXY -> mediator) then opens HTTP (TCP via REDIRECT ->
# mediator), so success of both exercises the full UDP+TCP capture pipeline.
"$CLI" connect "$WORKSPACE" --state-dir "$STATE_DIR" --ready-timeout 30 --timeout 40 --send \
  "wget -qO- -T 8 http://$HOST_A >/tmp/a 2>/dev/null; echo A_EXIT=\$?; wget -qO- -T 8 http://$HOST_B >/tmp/b 2>/dev/null; echo B_EXIT=\$?; echo A_BYTES=\$(wc -c </tmp/a); echo B_BYTES=\$(wc -c </tmp/b); sync" \
  >"$STATE_DIR/connect.txt" 2>&1

"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt.json"
sleep 2
if kill -0 "$mediator_pid" 2>/dev/null; then
  teardown="orphaned"
else
  teardown="clean"
fi

python3 - "$STATE_DIR/connect.txt" "$STATE_DIR/$WORKSPACE/egress-access.jsonl" "$HOST_A" "$HOST_B" "$teardown" <<'PY'
import json, re, sys
connect = open(sys.argv[1], encoding="utf-8", errors="replace").read()
audit_path, host_a, host_b, teardown = sys.argv[2:6]

def field(name):
    # The guest shell prefixes output (e.g. "~ # "), so match NAME=VALUE anywhere.
    m = re.search(r"\b" + re.escape(name) + r"=(\S+)", connect)
    return m.group(1) if m else None

# mediated allows ALL: both unrelated hosts must fetch successfully.
a_exit, b_exit = field("A_EXIT"), field("B_EXIT")
a_bytes, b_bytes = field("A_BYTES"), field("B_BYTES")
if a_exit != "0" or not (a_bytes and int(a_bytes) > 0):
    raise SystemExit(f"mediated did not allow {host_a}: {connect!r}")
if b_exit != "0" or not (b_bytes and int(b_bytes) > 0):
    raise SystemExit(f"mediated did not allow {host_b}: {connect!r}")

events = []
with open(audit_path, encoding="utf-8") as f:
    for ln in f:
        ln = ln.strip()
        if ln:
            events.append(json.loads(ln))

# UDP plane: the mediator opened a transparent UDP socket. Without this the
# TPROXY path has nowhere to deliver to — its presence proves the mediator serves
# UDP, the prerequisite for DNS mediation.
if not any(e.get("event") == "egress_udp_listen" for e in events):
    raise SystemExit(f"missing egress_udp_listen (mediator did not serve UDP): {events}")

# DNS mediated over the UDP/TPROXY plane: the guest's DNS datagrams were captured,
# forwarded, and answered. In mediated mode names are on no allowlist, so the
# grant is recorded as unlisted=true. Its presence is the end-to-end proof that
# guest UDP was TPROXY-captured AND the mediator is the resolver.
dns_allows = [e for e in events if e.get("event") == "egress_dns_allow"]
if not dns_allows:
    raise SystemExit(f"missing egress_dns_allow (DNS was not mediated): {events}")
if not any(e.get("unlisted") is True for e in dns_allows):
    raise SystemExit(f"mediated DNS grant not recorded as unlisted: {dns_allows}")
# Both hosts had to resolve through the mediator to be fetched.
resolved = {e.get("qname") for e in dns_allows}
for h in (host_a, host_b):
    if h not in resolved:
        raise SystemExit(f"host {h} was not resolved through the mediator (resolved={sorted(resolved)}): {events}")

# TCP plane: the captured HTTP connections were forwarded. Presence of
# egress_allow proves the REDIRECT/TCP capture path end-to-end (complementary to
# the UDP plane proven above).
tcp_allows = {e.get("host") for e in events if e.get("event") == "egress_allow"}
for h in (host_a, host_b):
    if h not in tcp_allows:
        raise SystemExit(f"missing egress_allow for {h} (TCP capture/forward path): {events}")

# A single benign egress_loop_guard is expected once per start (the supervisor's
# mediator-readiness self-probe dials the mediator's own listen addr; the loop
# guard drops it). Tolerate exactly that; flag anything beyond one.
loop_guards = sum(1 for e in events if e.get("event") == "egress_loop_guard")
if loop_guards > 1:
    raise SystemExit(f"unexpected extra egress_loop_guard events ({loop_guards}): {events}")

# mediated allows all: there must be no denials of either plane for these hosts.
if any(e.get("event") in ("egress_deny", "egress_dns_deny", "egress_udp_deny") for e in events):
    raise SystemExit(f"unexpected denial in mediated mode: {events}")

if teardown != "clean":
    raise SystemExit("egress mediator was orphaned after halt")
print("egress mediation (mediated default): UDP served, DNS mediated, TCP+UDP captured, all allowed, teardown clean")
PY

echo "microagent E2E egress-udp passed"
