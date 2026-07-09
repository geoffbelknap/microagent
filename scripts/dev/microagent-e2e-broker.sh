#!/usr/bin/env bash
set -euo pipefail

# egress broker: the workload reaches an upstream THROUGH the per-workspace
# broker with the credential injected host-side, with zero manual steps:
#   - `--broker-upstream/--broker-secret` on create/run wire everything: the
#     broker config persists in the manifest, the guest env (vsock bridge +
#     base URLs) is baked into the rootfs, and the supervisor serves the
#     broker on the workspace's vsock listener at start;
#   - the guest runs with NO network (isolated mode): its only path out is the
#     brokered vsock channel — containment and credential isolation compose;
#   - the workload sends a reference (`Authorization: Bearer @secret:api`);
#     the mock upstream confirms it received the LIVE credential, which the
#     guest never held (asserted against the guest's own environment);
#   - the broker access trail records the pre-swap reference and provably
#     never contains the live secret (absent by construction, not redaction).

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-broker.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
WORKSPACE="broker"
IMAGE="${MICROAGENT_E2E_BROKER_IMAGE:-docker.io/library/busybox:latest}"
LIVE_SECRET="live-broker-secret-$(date +%s)-$$"
UPSTREAM_PID=""

cleanup() {
  status="$?"
  if [ -n "$UPSTREAM_PID" ]; then
    kill "$UPSTREAM_PID" >/dev/null 2>&1 || true
  fi
  if [ -x "$CLI" ]; then
    "$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_BROKER:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E broker state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64) ;;
  *) e2e_skip "microagent E2E broker requires Linux amd64" ;;
esac

if [ ! -e /dev/kvm ]; then
  e2e_skip "/dev/kvm is not visible; run this smoke outside sandboxed environments"
fi
command -v python3 >/dev/null 2>&1 || e2e_skip "python3 is required for the broker E2E"

if [ -n "${MICROAGENT_FIRECRACKER:-}" ]; then
  firecracker="$MICROAGENT_FIRECRACKER"
elif command -v firecracker >/dev/null 2>&1; then
  firecracker="$(command -v firecracker)"
else
  firecracker=""
fi
if [ ! -x "${firecracker:-}" ]; then
  e2e_skip "Linux microagent E2E requires the Firecracker backend binary; install firecracker on PATH or set MICROAGENT_FIRECRACKER"
fi

export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
export GOFLAGS="${GOFLAGS:-} -modcacherw"
export MICROAGENT_FIRECRACKER="$firecracker"
export MICROAGENT_FIRECRACKER_SUPERVISOR="$SUPERVISOR"

echo "microagent E2E suite: broker"
echo "==> broker"

go build -buildvcs=false -o "$CLI" ./cmd/microagent
go build -buildvcs=false -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit

"$CLI" kernel install --backend linux-kvm --arch amd64 >"$STATE_DIR/kernel-install.json"

# Mock upstream on a host loopback port: 200 + marker when it sees the LIVE
# credential, 401 otherwise. It never echoes the secret back, so the guest can
# prove the upstream held the live value without ever receiving it.
cat >"$STATE_DIR/upstream.py" <<'PY'
import os, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

expected = "Bearer " + os.environ["MA_E2E_BROKER_TOKEN"]

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.headers.get("Authorization") == expected:
            body = b"UPSTREAM-SAW-LIVE-SECRET"
            self.send_response(200)
        else:
            body = b"NOMATCH"
            self.send_response(401)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass

server = HTTPServer(("127.0.0.1", 0), Handler)
with open(sys.argv[1], "w") as f:
    f.write(str(server.server_address[1]))
server.serve_forever()
PY
MA_E2E_BROKER_TOKEN="$LIVE_SECRET" python3 "$STATE_DIR/upstream.py" "$STATE_DIR/upstream.port" &
UPSTREAM_PID="$!"
for _ in $(seq 1 50); do
  [ -s "$STATE_DIR/upstream.port" ] && break
  sleep 0.1
done
[ -s "$STATE_DIR/upstream.port" ] || { echo "mock upstream did not start" >&2; exit 1; }
UPSTREAM_PORT="$(cat "$STATE_DIR/upstream.port")"

# Isolated network: the guest has NO NIC. Its only way to the upstream is the
# brokered vsock channel the create flags wired up. The workload sends the
# credential REFERENCE; it also dumps its environment so the host can assert
# the live secret never entered the guest.
#
# This command runs in the GUEST shell: the $(wget ...) substitution and $resp
# must expand there, not on this host, so the single quotes are deliberate.
# shellcheck disable=SC2016
GUEST_EXEC='resp="$(wget -qO- --header "Authorization: Bearer @secret:api" http://127.0.0.1:18888/check)" && echo "RESP:$resp"; echo GUEST-ENV-BEGIN; env; echo GUEST-ENV-END'
MA_E2E_BROKER_TOKEN="$LIVE_SECRET" "$CLI" --mode=ax run \
  --name "$WORKSPACE" \
  --image "$IMAGE" \
  --network isolated \
  --broker-upstream "http://127.0.0.1:$UPSTREAM_PORT" \
  --broker-secret "api=env:MA_E2E_BROKER_TOKEN" \
  --keep \
  --exec "$GUEST_EXEC" \
  --state-dir "$STATE_DIR" >"$STATE_DIR/run.json"

BROKER_TRAIL="$STATE_DIR/$WORKSPACE/broker-access.jsonl"
MA_E2E_BROKER_TOKEN="$LIVE_SECRET" python3 - \
  "$STATE_DIR/run.json" \
  "$BROKER_TRAIL" \
  "$STATE_DIR/workspaces/$WORKSPACE/workspace.json" <<'PY'
import json, os, sys

run_path, trail_path, manifest_path = sys.argv[1:4]
live = os.environ["MA_E2E_BROKER_TOKEN"]

with open(run_path) as f:
    run = json.load(f)
res = run.get("result") or {}
stdout = res.get("stdout") or ""
assert res.get("exit_code") == 0, f"expected exit_code 0, got {res.get('exit_code')}: {res}"

# The upstream saw the LIVE credential — injected by the broker, reached from
# a guest with no network of its own.
assert "RESP:UPSTREAM-SAW-LIVE-SECRET" in stdout, f"upstream marker missing: {stdout!r}"

# The guest itself never held the live secret: not in its environment, and
# nowhere else in anything it printed.
env_dump = stdout.split("GUEST-ENV-BEGIN", 1)[-1]
assert "GUEST-ENV-END" in env_dump, f"guest env dump incomplete: {stdout!r}"
assert live not in stdout, "INVARIANT VIOLATION: live secret visible inside the guest"

# The guest env carries the broker wiring the create flags baked in.
assert "MICROAGENT_VSOCK_TCP_LISTENERS=127.0.0.1:18888=1032" in env_dump, f"bridge env missing: {env_dump!r}"

# The broker access trail records the pre-swap reference and provably never
# the live secret (absent by construction, not redaction).
with open(trail_path) as f:
    trail = f.read()
assert "@secret:api" in trail, f"pre-swap reference missing from broker trail:\n{trail}"
assert live not in trail, f"INVARIANT VIOLATION: live secret present in broker trail:\n{trail}"

# The manifest persists the broker config — reference only — so every start
# re-arms the broker identically.
with open(manifest_path) as f:
    manifest = json.load(f)
broker = manifest.get("broker") or {}
assert broker.get("upstream", "").startswith("http://127.0.0.1:"), f"manifest broker = {broker}"
assert broker.get("secret", {}).get("ref") == "env:MA_E2E_BROKER_TOKEN", f"manifest broker secret = {broker.get('secret')}"
assert live not in json.dumps(manifest), "INVARIANT VIOLATION: live secret persisted in the manifest"

print("broker: guest with no network reached the upstream through the broker; credential injected host-side, never present in guest, trail, or manifest")
PY

echo "microagent E2E broker passed"
