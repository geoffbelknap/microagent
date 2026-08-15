#!/usr/bin/env bash
# microagent E2E: broker egress mode (transparent forward-proxy termination).
#
# Proves the broker enforcement path in rootless user/pasta mode:
#   - a public HTTPS host -> reached, but SPLICED not intercepted: the guest sees
#     the REAL upstream cert (issuer != our CA), and NO per-workspace CA is
#     delivered to the guest (no egress-ca-bundle.pem, no egress-ca.pem minted).
#   - an inside/metadata IP -> denied on the resolved IP (egress_internal_deny):
#     a direct-to-IP dial is still forced through the mediator and refused.
#   - the mediator is torn down with the workspace (no orphan).
#
# The contrast with the egress-mitm E2E is the whole point: mitm forges
# a per-SNI leaf (issuer = "microagent egress"); broker forges nothing.
#
# Runs in --network user (pasta): no host CAP_NET_ADMIN / root needed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh
. "$ROOT/scripts/dev/e2e-lib.sh"
export PATH="/home/linuxbrew/.linuxbrew/bin:/home/linuxbrew/.linuxbrew/sbin:/opt/homebrew/bin:/opt/homebrew/sbin:$PATH"

STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-egress-broker.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
WORKSPACE="egress-broker"
IMAGE="${MICROAGENT_EGRESS_BROKER_IMAGE:-docker.io/curlimages/curl:latest}"
PUBLIC_HOST="example.com" # reached via allow-broad, spliced (real cert)
INSIDE_IP="169.254.169.254" # cloud metadata; must be denied as inside

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    "$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_E2E_EGRESS_BROKER:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept egress-broker E2E state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64) ;;
  *) e2e_skip "egress-broker E2E requires Linux amd64" ;;
esac
for required in pasta python3; do
  command -v "$required" >/dev/null 2>&1 || e2e_skip "$required is required for the egress-broker E2E"
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
[ -x "${firecracker:-}" ] || e2e_skip "egress-broker E2E needs the Firecracker backend binary; install firecracker or set MICROAGENT_FIRECRACKER"

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

# Prepare a broker-egress workspace by writing the manifest directly (mirrors the
# egress-mitm E2E). Exercises the supervisor runtime path that provisions the
# mediator in broker mode: transparent redirect, no CA minted, opaque splice.
mkdir -p "$STATE_DIR/workspaces/$WORKSPACE" "$STATE_DIR/$WORKSPACE"
e2e_copy_workspace_rootfs "$rootfs_src" "$STATE_DIR/workspaces/$WORKSPACE/rootfs.ext4"
python3 - "$STATE_DIR" "$WORKSPACE" <<'PY'
import json, os, sys, time
state_dir, name = sys.argv[1:3]
now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
manifest = {
    "name": name, "profile": "small", "restart": "never",
    "resources": {"memory_mib": 512, "cpu_count": 2, "size_mib": 128},
    "network": {"mode": "user"},
    "egress_mode": "broker",
}
event = {
    "identity": {"requestID": name + "-prepared", "runtimeID": name, "role": "workload", "backend": "linux-kvm"},
    "state": "prepared", "detail": "prepared for egress-broker E2E", "observedAt": now,
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

# No CA should have been minted for a broker-mode workspace.
if [ -f "$STATE_DIR/$WORKSPACE/egress-ca.pem" ]; then
  echo "broker workspace minted a CA (egress-ca.pem present) — must forge nothing" >&2
  exit 1
fi

mediator_pid="$(pgrep -af 'egress[-]mediator' | grep "$STATE_DIR" | awk '{print $1}' | head -1 || true)"
[ -n "$mediator_pid" ] || { echo "no egress mediator process found" >&2; exit 1; }

"$CLI" connect "$WORKSPACE" --state-dir "$STATE_DIR" --ready-timeout 30 --timeout 50 --send \
  "echo BUNDLE=\$([ -f /etc/microagent/egress-ca-bundle.pem ] && echo yes || echo no); \
   curl -sS -m 12 https://$PUBLIC_HOST -o /dev/null -w 'PUB_CODE=%{http_code}\n' 2>&1; echo PUB_EXIT=\$?; \
   echo PUB_ISSUER=\$(curl -sS -m 12 -v https://$PUBLIC_HOST 2>&1 | sed -n 's/^\* *issuer: *//p' | head -1); \
   curl -sS -m 8 http://$INSIDE_IP -o /dev/null -w 'INSIDE_CODE=%{http_code}\n' 2>&1; echo INSIDE_EXIT=\$?; sync" \
  >"$STATE_DIR/connect.txt" 2>&1

"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt.json"
sleep 2
if kill -0 "$mediator_pid" 2>/dev/null; then teardown="orphaned"; else teardown="clean"; fi

python3 - "$STATE_DIR/connect.txt" "$STATE_DIR/$WORKSPACE/egress-access.jsonl" "$PUBLIC_HOST" "$INSIDE_IP" "$teardown" <<'PY'
import json, re, sys
connect = open(sys.argv[1], encoding="utf-8", errors="replace").read()
audit_path, public, inside, teardown = sys.argv[2:6]

def field(name):
    m = re.search(r"\b" + re.escape(name) + r"=(.*)", connect)
    return m.group(1).strip() if m else None

# No CA delivered to the guest: broker splices, it does not intercept.
if field("BUNDLE") != "no":
    raise SystemExit(f"broker delivered a CA bundle to the guest (must not): {connect!r}")

# Public host: reached (allow-broad) but SPLICED -> the REAL upstream cert.
if field("PUB_CODE") != "200" or field("PUB_EXIT") != "0":
    raise SystemExit(f"public host not reachable under broker allow-broad: {connect!r}")
pub_issuer = field("PUB_ISSUER") or ""
if not pub_issuer:
    raise SystemExit(f"public host produced no issuer line: {connect!r}")
if "microagent egress" in pub_issuer:
    raise SystemExit(f"public host WAS intercepted (issuer={pub_issuer!r}) — broker must splice, not forge")

# Inside/metadata IP: a direct-to-IP dial is forced through the mediator and denied.
if field("INSIDE_EXIT") == "0":
    raise SystemExit(f"inside IP was NOT denied: {connect!r}")

events = [json.loads(l) for l in open(audit_path, encoding="utf-8") if l.strip()]
# Public host allowed but NOT intercepted (no mitm=true), tagged unlisted (allow-broad).
if not any(e.get("event") == "egress_allow" and e.get("host") == public and not e.get("mitm") for e in events):
    raise SystemExit(f"missing non-intercepted egress_allow for {public}: {events}")
if any(e.get("event") == "egress_allow" and e.get("host") == public and e.get("mitm") for e in events):
    raise SystemExit(f"broker intercepted {public} (mitm=true) — must splice: {events}")
# Inside IP denied on the resolved IP (broker inside classification).
if not any(e.get("event") == "egress_internal_deny" for e in events):
    raise SystemExit(f"missing egress_internal_deny for the inside IP: {events}")

if teardown != "clean":
    raise SystemExit("egress mediator was orphaned after halt")
print(f"egress-broker: {public} reached + spliced (real cert, no CA in guest), {inside} denied as inside, teardown clean")
PY

echo "microagent E2E egress-broker passed"
