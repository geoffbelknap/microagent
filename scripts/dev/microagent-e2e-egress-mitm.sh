#!/usr/bin/env bash
# microagent E2E: strict egress with TLS interception (per-workspace CA) + passthrough.
#
# Proves the full Plan-3 pipeline in rootless user/pasta mode:
#   - allowlisted HTTPS host  -> intercepted: the guest trusts the per-workspace CA
#     delivered over vsock; the mediator presents a CA-signed leaf (issuer = our CA).
#   - passthrough HTTPS host  -> NOT intercepted: the guest sees the real upstream
#     cert (issuer != our CA), trusted via the combined CA bundle (system + our CA).
#   - non-allowlisted HTTPS   -> blocked fail-closed.
#   - the mediator is torn down with the workspace (no orphan).
#
# Runs in --network user (pasta): no host CAP_NET_ADMIN / root needed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh
. "$ROOT/scripts/dev/e2e-lib.sh"
export PATH="/home/linuxbrew/.linuxbrew/bin:/home/linuxbrew/.linuxbrew/sbin:/opt/homebrew/bin:/opt/homebrew/sbin:$PATH"

STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-egress-mitm.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
WORKSPACE="egress-mitm"
# curlimages/curl: alpine-based, has curl (TLS), /bin/sh, and a public CA bundle.
IMAGE="${MICROAGENT_EGRESS_MITM_IMAGE:-docker.io/curlimages/curl:latest}"
ALLOW_HOST="example.com"        # intercepted (MITM)
PASSTHROUGH_HOST="example.org"  # allowed, NOT intercepted
DENY_HOST="example.net"         # blocked

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    "$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_E2E_EGRESS_MITM:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept egress-mitm E2E state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64) ;;
  *) e2e_skip "egress-mitm E2E requires Linux amd64" ;;
esac
for required in pasta python3; do
  command -v "$required" >/dev/null 2>&1 || e2e_skip "$required is required for the egress-mitm E2E"
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
[ -x "${firecracker:-}" ] || e2e_skip "egress-mitm E2E needs the Firecracker backend binary; install firecracker or set MICROAGENT_FIRECRACKER"

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

"$CLI" kernel install --backend firecracker --arch amd64 >"$STATE_DIR/kernel-install.json"
kernel_path="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["path"])' "$STATE_DIR/kernel-install.json")"

echo "pulling $IMAGE -> rootfs" >&2
"$CLI" image pull "$IMAGE" \
  --state-dir "$STATE_DIR/cache" --arch amd64 \
  --guest-init "$GUEST_INIT" --size-mib 128 >"$STATE_DIR/image-pull.json"
rootfs_src="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["output_path"])' "$STATE_DIR/image-pull.json")"

# Prepare a strict-egress + passthrough workspace by writing the manifest directly
# (mirrors the networking E2E). The CLI->manifest plumbing is unit-tested; this
# exercises the supervisor runtime path that mints the CA, serves it over vsock,
# and wires the mediator for interception vs passthrough.
mkdir -p "$STATE_DIR/workspaces/$WORKSPACE" "$STATE_DIR/$WORKSPACE"
cp "$rootfs_src" "$STATE_DIR/workspaces/$WORKSPACE/rootfs.ext4"
python3 - "$STATE_DIR" "$WORKSPACE" "$ALLOW_HOST" "$PASSTHROUGH_HOST" <<'PY'
import json, os, sys, time
state_dir, name, allow, passthrough = sys.argv[1:5]
now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
manifest = {
    "name": name, "profile": "small", "restart": "never",
    "resources": {"memory_mib": 512, "cpu_count": 2, "size_mib": 128},
    "network": {"mode": "user"},
    "egress_mode": "strict", "egress_allow": [allow], "egress_passthrough": [passthrough],
}
event = {
    "identity": {"requestID": name + "-prepared", "runtimeID": name, "role": "workload", "backend": "firecracker"},
    "state": "prepared", "detail": "prepared for egress-mitm E2E", "observedAt": now,
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
[ -n "$mediator_pid" ] || { echo "no egress mediator process found" >&2; exit 1; }

"$CLI" connect "$WORKSPACE" --state-dir "$STATE_DIR" --ready-timeout 30 --timeout 50 --send \
  "echo BUNDLE=\$([ -f /etc/microagent/egress-ca-bundle.pem ] && echo yes || echo no); \
   curl -sS -m 12 https://$ALLOW_HOST -o /dev/null -w 'ALLOW_CODE=%{http_code}\n' 2>&1; echo ALLOW_EXIT=\$?; \
   echo ALLOW_ISSUER=\$(curl -sS -m 12 -v https://$ALLOW_HOST 2>&1 | sed -n 's/^\* *issuer: *//p' | head -1); \
   curl -sS -m 12 https://$PASSTHROUGH_HOST -o /dev/null -w 'PASS_CODE=%{http_code}\n' 2>&1; echo PASS_EXIT=\$?; \
   echo PASS_ISSUER=\$(curl -sS -m 12 -v https://$PASSTHROUGH_HOST 2>&1 | sed -n 's/^\* *issuer: *//p' | head -1); \
   curl -sS -m 12 https://$DENY_HOST -o /dev/null -w 'DENY_CODE=%{http_code}\n' 2>&1; echo DENY_EXIT=\$?; sync" \
  >"$STATE_DIR/connect.txt" 2>&1

"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt.json"
sleep 2
if kill -0 "$mediator_pid" 2>/dev/null; then teardown="orphaned"; else teardown="clean"; fi

python3 - "$STATE_DIR/connect.txt" "$STATE_DIR/$WORKSPACE/egress-access.jsonl" "$ALLOW_HOST" "$PASSTHROUGH_HOST" "$DENY_HOST" "$teardown" <<'PY'
import json, re, sys
connect = open(sys.argv[1], encoding="utf-8", errors="replace").read()
audit_path, allow, passt, deny, teardown = sys.argv[2:7]

def field(name):
    m = re.search(r"\b" + re.escape(name) + r"=(.*)", connect)
    return m.group(1).strip() if m else None

# Allowed host: intercepted -> success + issuer is OUR CA.
if field("BUNDLE") != "yes":
    raise SystemExit(f"combined CA bundle missing in guest: {connect!r}")
if field("ALLOW_CODE") != "200":
    raise SystemExit(f"allowlisted host not reachable: {connect!r}")
if field("ALLOW_EXIT") != "0":
    raise SystemExit(f"allowlisted curl failed: {connect!r}")
allow_issuer = field("ALLOW_ISSUER") or ""
if "microagent egress" not in allow_issuer:
    raise SystemExit(f"allowlisted host was NOT intercepted (issuer={allow_issuer!r})")

# Passthrough host: NOT intercepted -> success + issuer is the REAL public CA.
if field("PASS_CODE") != "200" or field("PASS_EXIT") != "0":
    raise SystemExit(f"passthrough host not reachable: {connect!r}")
pass_issuer = field("PASS_ISSUER") or ""
if "microagent egress" in pass_issuer:
    raise SystemExit(f"passthrough host WAS intercepted (issuer={pass_issuer!r}) — must be the real cert")
if not pass_issuer:
    raise SystemExit(f"passthrough host produced no issuer line: {connect!r}")

# Denied host: blocked.
if field("DENY_EXIT") == "0":
    raise SystemExit(f"non-allowlisted host was NOT blocked: {connect!r}")

# Audit log: allow host intercepted (mitm), passthrough not, deny recorded.
events = [json.loads(l) for l in open(audit_path, encoding="utf-8") if l.strip()]
def has(ev, host, **kw):
    return any(e.get("event") == ev and e.get("host") == host and all(e.get(k) == v for k, v in kw.items()) for e in events)
if not has("egress_allow", allow, mitm=True):
    raise SystemExit(f"missing intercepted egress_allow (mitm=true) for {allow}: {events}")
if not any(e.get("event") == "egress_allow" and e.get("host") == passt and not e.get("mitm") for e in events):
    raise SystemExit(f"missing non-intercepted egress_allow for {passt}: {events}")
if not has("egress_deny", deny):
    raise SystemExit(f"missing egress_deny for {deny}: {events}")

if teardown != "clean":
    raise SystemExit("egress mediator was orphaned after halt")
print(f"egress-mitm: {allow} intercepted (our CA), {passt} passthrough (real cert), {deny} blocked, teardown clean")
PY

echo "microagent E2E egress-mitm passed"
