#!/usr/bin/env bash
# microagent E2E: non-cooperation signals + the mitm load-time warning.
#
# Proves on a real Firecracker guest (rootless user/pasta):
#   - a broker-mode workspace whose guest dials a bare PUBLIC IP with no SNI is
#     allowed but tagged `signal: direct-ip-no-sni` in egress-access.jsonl — a
#     cooperative client resolves names first, so direct-to-IP is conspicuous;
#   - the same guest has a ULA plus an IPv6 default route, and a public IPv6 TCP
#     attempt reaches the mediator and is audited by its full IPv6 destination;
#   - an allowlist-locked broker workspace completes an HTTP/3 request and
#     audits its QUIC SNI as the destination host;
#   - a mitm-mode workspace logs `egress_mitm_enabled` at load time (the
#     sunsetting-mode warning is never silent) and DOES mint a CA, in contrast
#     to broker.
#
# The QUIC workspace also sends malformed UDP:443 and proves that the narrowed
# quic-udp443 signal reaches the live audit.
#
# Standalone/manual like the other egress E2Es: needs real internet + KVM, not
# in the auto-suite.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"
export PATH="/home/linuxbrew/.linuxbrew/bin:/home/linuxbrew/.linuxbrew/sbin:/opt/homebrew/bin:/opt/homebrew/sbin:$PATH"

STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-egress-signals.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
IMAGE="${MICROAGENT_EGRESS_SIGNALS_IMAGE:-docker.io/curlimages/curl:latest}"
QUIC_IMAGE="${MICROAGENT_EGRESS_QUIC_IMAGE:-docker.io/alpine/curl-http3:8.11.0}"
PUBLIC_IP="1.1.1.1" # a bare public IP: dialed with no SNI -> direct-ip-no-sni
PUBLIC_IPV6="2606:4700:4700::1111"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    for ws in signals-broker signals-quic signals-mitm; do
      "$CLI" halt "$ws" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      if [ "$status" -eq 0 ]; then
        "$CLI" delete "$ws" --yes --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      fi
    done
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_E2E_EGRESS_SIGNALS:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept egress-signals E2E state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64) ;;
  *) e2e_skip "egress-signals E2E requires Linux amd64" ;;
esac
for required in pasta python3; do
  command -v "$required" >/dev/null 2>&1 || e2e_skip "$required is required for the egress-signals E2E"
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
[ -x "${firecracker:-}" ] || e2e_skip "egress-signals E2E needs the Firecracker backend binary; install firecracker or set MICROAGENT_FIRECRACKER"

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

echo "pulling $QUIC_IMAGE -> HTTP/3 rootfs" >&2
"$CLI" image pull "$QUIC_IMAGE" \
  --state-dir "$STATE_DIR/cache" --arch amd64 \
  --guest-init "$GUEST_INIT" --size-mib 128 >"$STATE_DIR/quic-image-pull.json"
quic_rootfs_src="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["output_path"])' "$STATE_DIR/quic-image-pull.json")"

prepare_ws() {
  # prepare_ws <name> <egress_mode> <rootfs> [allow_host]: write a manifest + prepared event and copy
  # the rootfs, mirroring the other egress E2Es' direct-manifest approach.
  name="$1"; mode="$2"; rootfs="$3"; allow_host="${4:-}"
  mkdir -p "$STATE_DIR/workspaces/$name" "$STATE_DIR/$name"
  e2e_copy_workspace_rootfs "$rootfs" "$STATE_DIR/workspaces/$name/rootfs.ext4"
  python3 - "$STATE_DIR" "$name" "$mode" "$allow_host" <<'PY'
import json, os, sys, time
state_dir, name, mode, allow_host = sys.argv[1:5]
now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
manifest = {
    "name": name, "profile": "small", "restart": "never",
    "resources": {"memory_mib": 512, "cpu_count": 2, "size_mib": 128},
    "network": {"mode": "user"},
    "egress_mode": mode,
}
if allow_host:
    manifest["egress_allow"] = [allow_host]
    manifest["egress_allowlist_locked"] = True
event = {
    "identity": {"requestID": name + "-prepared", "runtimeID": name, "role": "workload", "backend": "linux-kvm"},
    "state": "prepared", "detail": "prepared for egress-signals E2E", "observedAt": now,
}
json.dump(manifest, open(os.path.join(state_dir, "workspaces", name, "workspace.json"), "w"), indent=2, sort_keys=True)
json.dump(event, open(os.path.join(state_dir, name, "event.json"), "w"), indent=2, sort_keys=True)
PY
}

wait_running() {
  name="$1"; deadline="$((SECONDS + 60))"
  while true; do
    state="$("$CLI" status "$name" --state-dir "$STATE_DIR" 2>/dev/null | python3 -c 'import sys,json;print((json.load(sys.stdin).get("event") or {}).get("state",""))' 2>/dev/null || true)"
    [ "$state" = "running" ] && break
    if [ "$state" = "failed" ] || [ "$SECONDS" -ge "$deadline" ]; then
      echo "workspace $name did not reach running (state=$state)" >&2
      exit 1
    fi
    sleep 1
  done
}

# --- broker workspace: direct-to-IP with no SNI -> direct-ip-no-sni -----------
prepare_ws signals-broker broker "$rootfs_src"
"$CLI" start signals-broker --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/start-broker.json"
wait_running signals-broker

# Dial a bare PUBLIC IP over HTTPS: curl sends no SNI for an IP literal, so the
# mediator sniffs no host and records the flow against the bare IP.
"$CLI" connect signals-broker --state-dir "$STATE_DIR" --ready-timeout 30 --timeout 40 --send \
  "curl -sS -k -m 12 https://$PUBLIC_IP/ -o /dev/null -w 'IP_CODE=%{http_code}\n' 2>&1; echo IP_EXIT=\$?" \
  >"$STATE_DIR/broker-guest.txt" 2>&1 || true
cat "$STATE_DIR/broker-guest.txt" >&2

# IPv6 is guest-local ULA transport into the mediator. The host may itself be on
# an IPv4-only network, so this leg proves guest configuration + capture + audit
# rather than requiring the upstream public IPv6 handshake to succeed.
"$CLI" connect signals-broker --state-dir "$STATE_DIR" --ready-timeout 30 --timeout 30 --send \
  "ip -6 addr show dev eth0; ip -6 route; curl -g -sS -k -m 8 https://[$PUBLIC_IPV6]/ -o /dev/null || true; printf v6-udp | /bin/busybox nc -u -w 1 $PUBLIC_IPV6 123 || true" \
  >"$STATE_DIR/broker-ipv6-guest.txt" 2>&1
cat "$STATE_DIR/broker-ipv6-guest.txt" >&2
grep -Eq 'fd00:6d69:6372:[0-9a-f]+::2/64' "$STATE_DIR/broker-ipv6-guest.txt" || { echo "guest has no managed IPv6 ULA" >&2; exit 1; }
grep -Eq '^default via fd00:6d69:6372:[0-9a-f]+::1' "$STATE_DIR/broker-ipv6-guest.txt" || { echo "guest has no managed IPv6 default route" >&2; exit 1; }

"$CLI" halt signals-broker --state-dir "$STATE_DIR" >/dev/null 2>&1 || true

python3 - "$STATE_DIR/signals-broker/egress-access.jsonl" <<'PY'
import json, sys
path = sys.argv[1]
signals = set()
records = []
try:
    for line in open(path):
        line = line.strip()
        if not line:
            continue
        try:
            rec = json.loads(line)
        except ValueError:
            continue
        records.append(rec)
        s = rec.get("signal")
        if s:
            signals.add(s)
except FileNotFoundError:
    print(f"no egress audit at {path}", file=sys.stderr); sys.exit(1)
assert "direct-ip-no-sni" in signals, f"direct-ip-no-sni not tagged; signals seen: {sorted(signals)}"
assert any("[2606:4700:4700::1111]:443" == r.get("dst") for r in records), "public IPv6 TCP attempt never reached the mediator audit"
assert any("[2606:4700:4700::1111]:123" == r.get("dst") for r in records), "public IPv6 UDP attempt never reached the mediator audit"
print("broker: bare-IP no-SNI tagged; guest IPv6 ULA/default route and mediated IPv6 TCP/UDP audit proved")
PY

# --- QUIC workspace: allowlisted HTTP/3 is mediated by ClientHello SNI -------
prepare_ws signals-quic broker "$quic_rootfs_src" cloudflare-quic.com
"$CLI" start signals-quic --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/start-quic.json"
wait_running signals-quic

"$CLI" connect signals-quic --state-dir "$STATE_DIR" --ready-timeout 30 --timeout 45 --send \
  "/usr/local/bin/curl --http3-only -sS -I --max-time 25 https://cloudflare-quic.com/ -o /dev/null -w 'HTTP_VERSION=%{http_version}\n'" \
  >"$STATE_DIR/quic-guest.txt" 2>&1
cat "$STATE_DIR/quic-guest.txt" >&2
grep -q 'HTTP_VERSION=3' "$STATE_DIR/quic-guest.txt" || { echo "guest HTTP/3 request did not negotiate HTTP/3" >&2; exit 1; }

# Send one malformed UDP:443 datagram after the valid request. It must fail
# closed and carry the narrowed quic-udp443 signal.
"$CLI" connect signals-quic --state-dir "$STATE_DIR" --ready-timeout 30 --timeout 10 --send \
  "printf 'not-a-quic-initial' | /bin/busybox nc -u -w 1 cloudflare-quic.com 443 || true" \
  >"$STATE_DIR/quic-invalid-guest.txt" 2>&1 || true

"$CLI" halt signals-quic --state-dir "$STATE_DIR" >/dev/null 2>&1 || true

python3 - "$STATE_DIR/signals-quic/egress-access.jsonl" <<'PY'
import json, sys
records = []
for line in open(sys.argv[1]):
    try:
        records.append(json.loads(line))
    except ValueError:
        pass
allows = [r for r in records if r.get("event") == "egress_udp_allow" and r.get("protocol") == "quic"]
assert any(r.get("host") == "cloudflare-quic.com" for r in allows), f"no QUIC SNI allow record: {allows}"
invalid = [r for r in records if r.get("signal") == "quic-udp443"]
assert invalid, "malformed UDP:443 was not audited with signal=quic-udp443"
print("broker: HTTP/3 completed with its SNI audited; malformed UDP:443 failed closed")
PY

# --- mitm workspace: egress_mitm_enabled warning + a CA is minted -------------
prepare_ws signals-mitm mitm "$rootfs_src"
"$CLI" start signals-mitm --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/start-mitm.json"
wait_running signals-mitm
"$CLI" halt signals-mitm --state-dir "$STATE_DIR" >/dev/null 2>&1 || true

# mitm forges certificates, so a CA IS minted (unlike broker).
[ -f "$STATE_DIR/signals-mitm/egress-ca.pem" ] || { echo "mitm workspace did not mint a CA (egress-ca.pem missing)" >&2; exit 1; }

python3 - "$STATE_DIR/signals-mitm/egress-access.jsonl" <<'PY'
import json, sys
path = sys.argv[1]
found = False
for line in open(path):
    line = line.strip()
    if not line:
        continue
    try:
        rec = json.loads(line)
    except ValueError:
        continue
    if rec.get("event") == "egress_mitm_enabled":
        found = True
        assert rec.get("warning"), f"egress_mitm_enabled carries no warning: {rec}"
assert found, "mitm workspace did not log egress_mitm_enabled"
print("mitm: egress_mitm_enabled warning logged at load time; CA minted")
PY

echo "microagent E2E egress-signals passed"
