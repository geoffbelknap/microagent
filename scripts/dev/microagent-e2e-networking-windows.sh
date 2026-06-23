#!/usr/bin/env bash
set -euo pipefail

# windows-hyperv arm of the networking-deep scenario: the backend-neutral
# networking feature contract on Hyper-V. Always covers network mode
# validation, isolated-mode semantics (no NIC, working loopback), the
# network status surface, and the live-apply guard rails. The HNS segments
# — user-mode boot with published ports, the live host-bind apply, and
# listener cleanup — need an elevated shell (HNS NAT network creation) and
# run on the elevated CI runner; a non-elevated host logs them as deferred.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

e2e_is_windows || e2e_skip "windows-hyperv networking E2E requires a Windows host"
e2e_have_hcs || e2e_skip "Hyper-V HCS services (vmms/vmcompute) are not running"
for required in go python3; do
  e2e_require_cmd "$required" "$required is required for windows-hyperv networking E2E"
done

STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-networking-whv.XXXXXX")"
CLI="$STATE_DIR/microagent.exe"
WS_ISOLATED="net-whv-isolated"
WS_NAT="net-whv-nat"
KEEP_VAR="${MICROAGENT_KEEP_MICROAGENT_E2E_NETWORKING:-0}"
IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f}"
KERNEL="$HOME/.microagent/kernels/windows-hyperv/amd64/Image"
PUBLISH_PORT="${MICROAGENT_E2E_PUBLISH_PORT:-18099}"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    for ws in "$WS_ISOLATED" "$WS_NAT"; do
      "$CLI" kill "$ws" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      "$CLI" delete "$ws" --yes --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    done
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "$KEEP_VAR" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent windows networking E2E state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
export GOFLAGS="${GOFLAGS:-} -modcacherw"

json_get() {
  python3 - "$1" "$2" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    value = json.load(f)
for part in sys.argv[2].split("."):
    if not part:
        continue
    if isinstance(value, list):
        value = value[int(part)]
    else:
        value = value[part]
print(value)
PY
}

expect_failure() {
  name="$1"
  expected="$2"
  shift 2
  if "$@" >"$STATE_DIR/$name.out" 2>"$STATE_DIR/$name.err"; then
    echo "$name unexpectedly succeeded" >&2
    exit 1
  fi
  if ! grep -Eiq -- "$expected" "$STATE_DIR/$name.out" "$STATE_DIR/$name.err"; then
    echo "$name failed without expected message: $expected" >&2
    cat "$STATE_DIR/$name.out" "$STATE_DIR/$name.err" >&2
    exit 1
  fi
}

wait_for_ready() {
  workspace="$1"
  output="$2"
  deadline="$((SECONDS + 45))"
  while true; do
    "$CLI" status "$workspace" --state-dir "$STATE_DIR" >"$output"
    if python3 - "$output" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    status = json.load(handle)
event = status.get("event") or {}
readiness = status.get("readiness") or {}
if (
    event.get("state") == "running"
    and readiness.get("execReady", {}).get("ready")
    and readiness.get("shellReady", {}).get("ready")
):
    raise SystemExit(0)
raise SystemExit(1)
PY
    then
      return 0
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "workspace $workspace did not become exec/shell ready" >&2
      cat "$output" >&2
      return 1
    fi
    sleep 1
  done
}

e2e_step "build CLI and guest init"
( cd "$ROOT"; go build -buildvcs=false -o "$CLI" ./cmd/microagent )
GUEST_INIT="$STATE_DIR/microagent-guestinit"
( cd "$ROOT"; GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit )

e2e_step "kernel artifact"
if [ ! -r "$KERNEL" ]; then
  "$CLI" kernel install || e2e_skip "windows-hyperv kernel install failed"
fi
test -r "$KERNEL"

e2e_step "network mode and publish validation failures"
expect_failure invalid-mode "network.mode" \
  "$CLI" create bad-mode --dry-run --image "$IMAGE" --network made-up --state-dir "$STATE_DIR"
expect_failure invalid-publish "publish" \
  "$CLI" create bad-publish --dry-run --image "$IMAGE" --network user --publish bad-mapping --state-dir "$STATE_DIR"
expect_failure publish-isolated "require user mode" \
  "$CLI" create bad-isolated-publish --dry-run --image "$IMAGE" --network isolated --publish "127.0.0.1:$PUBLISH_PORT:8080" --state-dir "$STATE_DIR"

e2e_step "isolated mode boots with no NIC and a working loopback"
"$CLI" create "$WS_ISOLATED" --image "$IMAGE" --network isolated --size-mib 512 \
  --service-command "mkdir -p /www && echo NET_WHV_LOOPBACK_OK > /www/index.html && httpd -f -p 8080 -h /www" \
  --state-dir "$STATE_DIR" >"$STATE_DIR/create-isolated.json"
test "$(json_get "$STATE_DIR/create-isolated.json" network.mode)" = "isolated"
"$CLI" start "$WS_ISOLATED" --state-dir "$STATE_DIR" >"$STATE_DIR/start-isolated.json"
wait_for_ready "$WS_ISOLATED" "$STATE_DIR/status-isolated.json"
# No non-loopback interface exists, and the in-guest loopback serves TCP.
"$CLI" exec "$WS_ISOLATED" --state-dir "$STATE_DIR" -- \
  sh -c "ls /sys/class/net; for i in \$(seq 1 10); do R=\$(wget -qO- http://127.0.0.1:8080/) && { echo \"LOOPBACK: \$R\"; exit 0; }; sleep 1; done; echo LOOPBACK_FAIL; exit 1" >"$STATE_DIR/exec-isolated.txt"
grep -q "LOOPBACK: NET_WHV_LOOPBACK_OK" "$STATE_DIR/exec-isolated.txt"
if grep -Eq '^(eth|en)' "$STATE_DIR/exec-isolated.txt"; then
  e2e_fail "isolated workspace has a non-loopback interface: $(cat "$STATE_DIR/exec-isolated.txt")"
fi

e2e_step "network status reports the isolated mode"
"$CLI" --json network "$WS_ISOLATED" --state-dir "$STATE_DIR" >"$STATE_DIR/network-isolated.json"
grep -q '"isolated"' "$STATE_DIR/network-isolated.json"

e2e_step "live apply guard rails reject non-host-bind changes"
cat >"$STATE_DIR/apply-mode-change.yaml" <<YAML
name: $WS_ISOLATED
network:
  mode: user
YAML
expect_failure apply-mode-change "host bind changes" \
  "$CLI" apply --file "$STATE_DIR/apply-mode-change.yaml" --state-dir "$STATE_DIR"

"$CLI" halt "$WS_ISOLATED" --state-dir "$STATE_DIR" >"$STATE_DIR/halt-isolated.json"
"$CLI" delete "$WS_ISOLATED" --yes --state-dir "$STATE_DIR" >/dev/null

if ! e2e_is_windows_elevated; then
  e2e_log "HNS segments deferred: user-mode publish, live host-bind apply, and listener cleanup need an elevated shell (HNS NAT); the elevated CI runner covers them"
  echo "microagent windows-hyperv networking E2E passed (non-elevated subset)"
  exit 0
fi

e2e_step "user mode publishes a guest port to the host"
"$CLI" create "$WS_NAT" --image "$IMAGE" --network user --size-mib 512 \
  --publish "127.0.0.1:$PUBLISH_PORT:8080" \
  --service-command "mkdir -p /www && echo NET_WHV_PUBLISH_OK > /www/index.html && httpd -f -p 8080 -h /www" \
  --state-dir "$STATE_DIR" >"$STATE_DIR/create-nat.json"
"$CLI" start "$WS_NAT" --state-dir "$STATE_DIR" >"$STATE_DIR/start-nat.json"
wait_for_ready "$WS_NAT" "$STATE_DIR/status-nat.json"
published=""
for _ in $(seq 1 20); do
  if published="$(curl -fsS "http://127.0.0.1:$PUBLISH_PORT/" 2>/dev/null)"; then
    break
  fi
  sleep 1
done
if [ "$published" != "NET_WHV_PUBLISH_OK" ]; then
  e2e_fail "published port did not serve the guest content: ${published:-no response}"
fi

e2e_step "user mode records the HNS endpoint address host-side"
"$CLI" --json network "$WS_NAT" --state-dir "$STATE_DIR" >"$STATE_DIR/network-nat.json"
grep -q '"user"' "$STATE_DIR/network-nat.json"
grep -q '192\.168\.' "$STATE_DIR/network-nat.json"

e2e_step "guest has an addressed NIC and a default route in user mode"
# hv_netvsc (kernels-6.12.22-r2) exposes the HNS endpoint as eth0 and the
# boot args carry the endpoint's static config.
"$CLI" exec "$WS_NAT" --state-dir "$STATE_DIR" -- \
  sh -c "ifconfig eth0 && route -n" >"$STATE_DIR/exec-nat-net.txt"
grep -q "inet addr:192.168." "$STATE_DIR/exec-nat-net.txt"
grep -Eq "^0\.0\.0\.0[[:space:]]+192\.168\." "$STATE_DIR/exec-nat-net.txt"

e2e_step "apply live-reloads a host bind change for the existing forward"
cat >"$STATE_DIR/apply-bind.yaml" <<YAML
name: $WS_NAT
network:
  mode: user
  forwards:
    - host: 0.0.0.0
      hostPort: $PUBLISH_PORT
      guestPort: 8080
      protocol: tcp
YAML
"$CLI" --json apply --file "$STATE_DIR/apply-bind.yaml" --state-dir "$STATE_DIR" >"$STATE_DIR/apply-bind.json"
test "$(json_get "$STATE_DIR/apply-bind.json" reloaded)" = "True"
applied=""
for _ in $(seq 1 20); do
  if applied="$(curl -fsS "http://127.0.0.1:$PUBLISH_PORT/" 2>/dev/null)"; then
    break
  fi
  sleep 1
done
if [ "$applied" != "NET_WHV_PUBLISH_OK" ]; then
  e2e_fail "published port did not serve after live apply: ${applied:-no response}"
fi
"$CLI" status "$WS_NAT" --state-dir "$STATE_DIR" >"$STATE_DIR/status-applied.json"
test "$(json_get "$STATE_DIR/status-applied.json" event.state)" = "running"

e2e_step "halt tears the published listener down"
"$CLI" halt "$WS_NAT" --state-dir "$STATE_DIR" >"$STATE_DIR/halt-nat.json"
if curl -fsS --max-time 3 "http://127.0.0.1:$PUBLISH_PORT/" >/dev/null 2>&1; then
  e2e_fail "published port still serves after halt"
fi
"$CLI" delete "$WS_NAT" --yes --state-dir "$STATE_DIR" >/dev/null

echo "microagent windows-hyperv networking E2E passed"
