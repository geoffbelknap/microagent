#!/usr/bin/env bash
# microagent E2E: locked-allowlist transparent egress mediation (rootless user/pasta mode).
#
# Proves that with `egress_mode: broker` + a locked allowlist, the mediator is the
# authoritative resolver: it forwards+answers DNS for an allowlisted name
# (egress_dns_allow) so that host is reachable (the captured TCP is then
# forwarded — egress_allow), and it REFUSES DNS for a non-allowlisted name
# (egress_dns_deny) so the guest cannot even resolve it — that host is
# unreachable WITHOUT any TCP attempt (so NO egress_deny is emitted; the block is
# at the DNS layer, before any connection). It also proves the mediator process
# is torn down with the workspace (no orphan).
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
WORKSPACE="egress-strict"
IMAGE="${MICROAGENT_EGRESS_IMAGE:-docker.io/library/alpine:3.20}"
ALLOW_HOST="example.com"
DENY_HOST="example.org"

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
    echo "kept microagent egress E2E state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64) ;;
  *) e2e_skip "microagent egress E2E requires Linux amd64" ;;
esac

for required in pasta python3; do
  command -v "$required" >/dev/null 2>&1 || e2e_skip "$required is required for the egress E2E"
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
[ -x "${firecracker:-}" ] || e2e_skip "egress E2E needs the Firecracker backend binary; install firecracker or set MICROAGENT_FIRECRACKER"

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

# Prepare a strict-egress workspace by writing the manifest directly (mirrors the
# networking E2E's prepare_cached_workspace). The CLI flag -> manifest plumbing is
# covered by unit tests (TestManifestRoundTripPreservesEgress); this exercises the
# supervisor runtime path that reads egress_mode/egress_allow from the manifest.
mkdir -p "$STATE_DIR/workspaces/$WORKSPACE" "$STATE_DIR/$WORKSPACE"
cp "$rootfs_src" "$STATE_DIR/workspaces/$WORKSPACE/rootfs.ext4"
python3 - "$STATE_DIR" "$WORKSPACE" "$ALLOW_HOST" <<'PY'
import json, os, sys, time
state_dir, name, allow = sys.argv[1:4]
now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
manifest = {
    "name": name, "profile": "small", "restart": "never",
    "resources": {"memory_mib": 512, "cpu_count": 2, "size_mib": 128},
    "network": {"mode": "user"},
    "egress_mode": "broker", "egress_allowlist_locked": True, "egress_allow": [allow],
}
event = {
    "identity": {"requestID": name + "-prepared", "runtimeID": name,
                 "role": "workload", "backend": "linux-kvm"},
    "state": "prepared", "detail": "prepared for egress E2E", "observedAt": now,
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

"$CLI" connect "$WORKSPACE" --state-dir "$STATE_DIR" --ready-timeout 30 --timeout 30 --send \
  "wget -qO- -T 8 http://$ALLOW_HOST >/tmp/allowed 2>/dev/null; echo ALLOWED_EXIT=\$?; wget -qO- -T 8 http://$DENY_HOST >/tmp/denied 2>/dev/null; echo DENIED_EXIT=\$?; echo ALLOWED_BYTES=\$(wc -c </tmp/allowed); echo DENIED_BYTES=\$(wc -c </tmp/denied); sync" \
  >"$STATE_DIR/connect.txt" 2>&1

"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt.json"
sleep 2
if kill -0 "$mediator_pid" 2>/dev/null; then
  teardown="orphaned"
else
  teardown="clean"
fi

python3 - "$STATE_DIR/connect.txt" "$STATE_DIR/$WORKSPACE/egress-access.jsonl" "$ALLOW_HOST" "$DENY_HOST" "$teardown" <<'PY'
import json, re, sys
connect = open(sys.argv[1], encoding="utf-8", errors="replace").read()
audit_path, allow, deny, teardown = sys.argv[2:6]

def field(name):
    # The guest shell prefixes output (e.g. "~ # "), so match NAME=VALUE anywhere.
    m = re.search(r"\b" + re.escape(name) + r"=(\S+)", connect)
    return m.group(1) if m else None

allowed_exit, denied_exit = field("ALLOWED_EXIT"), field("DENIED_EXIT")
allowed_bytes, denied_bytes = field("ALLOWED_BYTES"), field("DENIED_BYTES")
if allowed_exit != "0" or not (allowed_bytes and int(allowed_bytes) > 0):
    raise SystemExit(f"allowlisted host was not reachable: {connect!r}")
if denied_exit == "0" or (denied_bytes and int(denied_bytes) != 0):
    raise SystemExit(f"non-allowlisted host was NOT blocked: {connect!r}")

events = []
with open(audit_path, encoding="utf-8") as f:
    for ln in f:
        ln = ln.strip()
        if ln:
            events.append(json.loads(ln))

# The mediator is the authoritative resolver in strict mode. The allow is proven
# at BOTH layers: DNS for the allowlisted name is forwarded+answered
# (egress_dns_allow) and the captured TCP to it is then forwarded (egress_allow).
allow_dns_ok = any(e.get("event") == "egress_dns_allow" and e.get("qname") == allow for e in events)
allow_tcp_ok = any(e.get("event") == "egress_allow" and e.get("host") == allow for e in events)
if not allow_dns_ok:
    raise SystemExit(f"missing egress_dns_allow for {allow}: {events}")
if not allow_tcp_ok:
    raise SystemExit(f"missing egress_allow for {allow}: {events}")

# The block happens at the DNS layer: strict REFUSES the non-allowlisted name
# (egress_dns_deny) so the guest never learns an IP and never opens a TCP
# connection. Therefore the deny must be asserted via egress_dns_deny — and there
# must be NO egress_deny for it, because no TCP attempt is ever made.
deny_dns_ok = any(e.get("event") == "egress_dns_deny" and e.get("qname") == deny for e in events)
if not deny_dns_ok:
    raise SystemExit(f"missing egress_dns_deny for {deny} (a locked allowlist blocks at the DNS layer): {events}")
if any(e.get("event") == "egress_deny" and e.get("host") == deny for e in events):
    raise SystemExit(f"unexpected egress_deny for {deny}: a locked allowlist must block at the DNS layer, before any TCP: {events}")

# A single benign egress_loop_guard is expected once per start (the supervisor's
# mediator-readiness self-probe dials the mediator's own listen addr; the loop
# guard drops it). Tolerate exactly that; flag anything beyond one.
loop_guards = sum(1 for e in events if e.get("event") == "egress_loop_guard")
if loop_guards > 1:
    raise SystemExit(f"unexpected extra egress_loop_guard events ({loop_guards}): {events}")

if teardown != "clean":
    raise SystemExit("egress mediator was orphaned after halt")
print("egress mediation (broker + locked allowlist): allow resolved+forwarded, deny blocked at DNS layer, audit recorded, teardown clean")
PY

echo "microagent E2E egress passed"
