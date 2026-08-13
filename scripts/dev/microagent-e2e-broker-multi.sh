#!/usr/bin/env bash
set -euo pipefail

# egress broker, multi-endpoint: ONE workload reaching TWO credentialed
# upstreams through TWO independent broker endpoints in the same workspace:
#   - repeatable `--broker-endpoint "upstream=...;secret=NAME=<scheme>:<ref>;
#     base-url-env=KEY"` declares each endpoint fully on its own; the two
#     endpoints get distinct guest-listens and vsock ports assigned
#     automatically (normalizeBrokers), so they never collide;
#   - the guest reaches each endpoint via its OWN base-URL env
#     (UPSTREAM_A_URL / UPSTREAM_B_URL) — it never needs to know the
#     auto-assigned listen address, only the env var the endpoint pointed at
#     it;
#   - each endpoint injects its OWN credential host-side; the guest holds only
#     `@secret:apiA` / `@secret:apiB` references, and a reference from one
#     endpoint is never resolvable against the other's upstream;
#   - both endpoints' decisions land in the ONE shared, minimized broker
#     trail, distinguished by upstream host, and are live via
#     `microagent egress`;
#   - both live credentials are absent from every file in the workspace
#     state, and the manifest persists both endpoints as the multi-endpoint
#     `brokers` list so a restart re-arms every endpoint identically.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-broker-multi.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
WORKSPACE="broker-multi"
IMAGE="${MICROAGENT_E2E_BROKER_MULTI_IMAGE:-docker.io/library/busybox:latest}"
LIVE_SECRET_A="live-broker-secret-a-$(date +%s)-$$"
LIVE_SECRET_B="live-broker-secret-b-$(date +%s)-$$"
UPSTREAM_A_PID=""
UPSTREAM_B_PID=""

cleanup() {
  status="$?"
  if [ -n "$UPSTREAM_A_PID" ]; then
    kill "$UPSTREAM_A_PID" >/dev/null 2>&1 || true
  fi
  if [ -n "$UPSTREAM_B_PID" ]; then
    kill "$UPSTREAM_B_PID" >/dev/null 2>&1 || true
  fi
  if [ -x "$CLI" ]; then
    "$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_BROKER_MULTI:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E broker-multi state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64) ;;
  *) e2e_skip "microagent E2E broker-multi requires Linux amd64" ;;
esac

if [ ! -e /dev/kvm ]; then
  e2e_skip "/dev/kvm is not visible; run this smoke outside sandboxed environments"
fi
command -v python3 >/dev/null 2>&1 || e2e_skip "python3 is required for the broker-multi E2E"

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

echo "microagent E2E suite: broker-multi"
echo "==> broker-multi"

go build -buildvcs=false -o "$CLI" ./cmd/microagent
go build -buildvcs=false -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit

"$CLI" kernel install --backend linux-kvm --arch amd64 >"$STATE_DIR/kernel-install.json"

# Two mock upstreams on two host loopback ports, each with its OWN distinct
# live secret: each returns ITS OWN marker only when it sees ITS OWN live
# credential, so the two endpoints can never be satisfied by each other's
# secret. sys.argv: token-env-var-name, marker, port-file.
cat >"$STATE_DIR/upstream.py" <<'PY'
import os, sys
from http.server import BaseHTTPRequestHandler, HTTPServer

token_env, marker, port_file = sys.argv[1], sys.argv[2], sys.argv[3]
expected = "Bearer " + os.environ[token_env]
marker_bytes = marker.encode()

class Handler(BaseHTTPRequestHandler):
    def _answer(self):
        length = int(self.headers.get("Content-Length") or 0)
        if length:
            self.rfile.read(length)
        if self.headers.get("Authorization") == expected:
            body = marker_bytes
            self.send_response(200)
        else:
            body = b"NOMATCH"
            self.send_response(401)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    do_GET = _answer
    do_POST = _answer

    def log_message(self, *args):
        pass

server = HTTPServer(("127.0.0.1", 0), Handler)
with open(port_file, "w") as f:
    f.write(str(server.server_address[1]))
server.serve_forever()
PY

MA_E2E_TOKEN_A="$LIVE_SECRET_A" python3 "$STATE_DIR/upstream.py" MA_E2E_TOKEN_A UPSTREAM-A-SAW-LIVE-SECRET "$STATE_DIR/upstream-a.port" &
UPSTREAM_A_PID="$!"
MA_E2E_TOKEN_B="$LIVE_SECRET_B" python3 "$STATE_DIR/upstream.py" MA_E2E_TOKEN_B UPSTREAM-B-SAW-LIVE-SECRET "$STATE_DIR/upstream-b.port" &
UPSTREAM_B_PID="$!"
for _ in $(seq 1 50); do
  [ -s "$STATE_DIR/upstream-a.port" ] && [ -s "$STATE_DIR/upstream-b.port" ] && break
  sleep 0.1
done
[ -s "$STATE_DIR/upstream-a.port" ] || { echo "mock upstream A did not start" >&2; exit 1; }
[ -s "$STATE_DIR/upstream-b.port" ] || { echo "mock upstream B did not start" >&2; exit 1; }
UPSTREAM_A_PORT="$(cat "$STATE_DIR/upstream-a.port")"
UPSTREAM_B_PORT="$(cat "$STATE_DIR/upstream-b.port")"

# Isolated network: the guest has NO NIC. Its only way out to EITHER upstream
# is a brokered vsock channel — one per endpoint. The workload sends each
# endpoint's credential REFERENCE via that endpoint's own base-URL env; it
# also dumps its environment so the host can assert neither live secret ever
# entered the guest.
#
# This command runs in the GUEST shell: the $(wget ...) substitutions and
# $UPSTREAM_A_URL/$UPSTREAM_B_URL/$resp_a/$resp_b must expand there, not on
# this host, so the single quotes are deliberate.
# shellcheck disable=SC2016
GUEST_EXEC='resp_a="$(wget -qO- --post-data "guest-request-body-a" --header "Authorization: Bearer @secret:apiA" "$UPSTREAM_A_URL/check")" && echo "RESP_A:$resp_a"; resp_b="$(wget -qO- --post-data "guest-request-body-b" --header "Authorization: Bearer @secret:apiB" "$UPSTREAM_B_URL/check")" && echo "RESP_B:$resp_b"; echo GUEST-ENV-BEGIN; env; echo GUEST-ENV-END'
MA_E2E_TOKEN_A="$LIVE_SECRET_A" MA_E2E_TOKEN_B="$LIVE_SECRET_B" "$CLI" --json run \
  --name "$WORKSPACE" \
  --image "$IMAGE" \
  --network isolated \
  --broker-endpoint "upstream=http://127.0.0.1:$UPSTREAM_A_PORT;secret=apiA=env:MA_E2E_TOKEN_A;assurance=trusted-upstream;base-url-env=UPSTREAM_A_URL" \
  --broker-endpoint "upstream=http://127.0.0.1:$UPSTREAM_B_PORT;secret=apiB=env:MA_E2E_TOKEN_B;assurance=trusted-upstream;base-url-env=UPSTREAM_B_URL" \
  --keep \
  --exec "$GUEST_EXEC" \
  --state-dir "$STATE_DIR" >"$STATE_DIR/run.json"

# The merged live view surfaces both endpoints' broker decisions.
"$CLI" egress "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/egress-view.txt"

BROKER_TRAIL="$STATE_DIR/$WORKSPACE/broker-access.jsonl"
MA_E2E_TOKEN_A="$LIVE_SECRET_A" MA_E2E_TOKEN_B="$LIVE_SECRET_B" python3 - \
  "$STATE_DIR/run.json" \
  "$BROKER_TRAIL" \
  "$STATE_DIR/workspaces/$WORKSPACE/workspace.json" \
  "$STATE_DIR/$WORKSPACE" \
  "$STATE_DIR/egress-view.txt" \
  "$UPSTREAM_A_PORT" \
  "$UPSTREAM_B_PORT" <<'PY'
import json, os, stat, sys

run_path, trail_path, manifest_path, ws_state_dir, view_path, port_a, port_b = sys.argv[1:8]
live_a = os.environ["MA_E2E_TOKEN_A"]
live_b = os.environ["MA_E2E_TOKEN_B"]
host_a = "127.0.0.1:" + port_a
host_b = "127.0.0.1:" + port_b

with open(run_path) as f:
    run = json.load(f)
res = run.get("result") or {}
stdout = res.get("stdout") or ""
assert res.get("exit_code") == 0, f"expected exit_code 0, got {res.get('exit_code')}: {res}"

# Each endpoint's upstream saw ITS OWN live credential — injected by that
# endpoint's broker listener, reached from a guest with no network of its own.
assert "RESP_A:UPSTREAM-A-SAW-LIVE-SECRET" in stdout, f"endpoint A upstream marker missing: {stdout!r}"
assert "RESP_B:UPSTREAM-B-SAW-LIVE-SECRET" in stdout, f"endpoint B upstream marker missing: {stdout!r}"

# The guest itself never held either live secret: not in its environment, and
# nowhere else in anything it printed.
env_dump = stdout.split("GUEST-ENV-BEGIN", 1)[-1]
assert "GUEST-ENV-END" in env_dump, f"guest env dump incomplete: {stdout!r}"
assert live_a not in stdout, "INVARIANT VIOLATION: endpoint A live secret visible inside the guest"
assert live_b not in stdout, "INVARIANT VIOLATION: endpoint B live secret visible inside the guest"

# Each endpoint's guest env wiring: its own base-URL env pointed at its own
# auto-assigned guest listener, and both vsock bridge listeners merged into
# the one bridge env the guest's init reads.
assert "UPSTREAM_A_URL=http://127.0.0.1:18888" in env_dump, f"endpoint A base URL env missing: {env_dump!r}"
assert "UPSTREAM_B_URL=http://127.0.0.1:18889" in env_dump, f"endpoint B base URL env missing: {env_dump!r}"
assert "MICROAGENT_VSOCK_TCP_LISTENERS=127.0.0.1:18888=1032,127.0.0.1:18889=1033" in env_dump, f"bridge env missing both endpoints: {env_dump!r}"

# The shared broker access trail carries minimized decisions for BOTH
# upstream hosts, distinguished by host — never the live secrets, exactly as
# the single-endpoint broker's trail is.
with open(trail_path) as f:
    trail = f.read()
assert "broker_request_allow" in trail, f"decision record missing from broker trail:\n{trail}"
audit_rows = [json.loads(line) for line in trail.splitlines() if line.strip()]
allow_by_host = {
    row.get("host"): row
    for row in audit_rows
    if row.get("event") == "broker_request_allow"
}
assert f'"host":"{host_a}"' in trail, f"endpoint A host missing from broker trail:\n{trail}"
assert f'"host":"{host_b}"' in trail, f"endpoint B host missing from broker trail:\n{trail}"
assert allow_by_host.get(host_a, {}).get("assurance") == "trusted-upstream", \
    f"endpoint A assurance missing from broker audit rows: {allow_by_host}"
assert allow_by_host.get(host_b, {}).get("assurance") == "trusted-upstream", \
    f"endpoint B assurance missing from broker audit rows: {allow_by_host}"
assert '"secret_refs":["apiA"]' in trail, f"endpoint A credential-use metadata missing:\n{trail}"
assert '"secret_refs":["apiB"]' in trail, f"endpoint B credential-use metadata missing:\n{trail}"
for banned in ("@secret:apiA", "@secret:apiB", "/check", '"headers"', "guest-request-body-a", "guest-request-body-b", live_a, live_b):
    assert banned not in trail, f"default trail must be minimized metadata, found {banned!r}:\n{trail}"

# The merged `microagent egress` view surfaces both endpoints' decisions live.
with open(view_path) as f:
    view = f.read()
assert "broker_request_allow" in view, f"broker decisions missing from egress view:\n{view}"
assert host_a in view, f"endpoint A host missing from egress view:\n{view}"
assert host_b in view, f"endpoint B host missing from egress view:\n{view}"

# The manifest persists BOTH endpoints as the multi-endpoint `brokers` list
# (not the single-endpoint `broker` field) — reference only — so every start
# re-arms every endpoint identically.
with open(manifest_path) as f:
    manifest = json.load(f)
brokers = manifest.get("brokers") or []
assert len(brokers) == 2, f"expected 2 persisted broker endpoints, got {len(brokers)}: {brokers}"
by_secret = {b.get("secret", {}).get("name"): b for b in brokers}
assert set(by_secret) == {"apiA", "apiB"}, f"manifest brokers = {brokers}"
assert by_secret["apiA"].get("upstream") == f"http://{host_a}", f"endpoint A upstream mismatch: {brokers}"
assert by_secret["apiB"].get("upstream") == f"http://{host_b}", f"endpoint B upstream mismatch: {brokers}"
assert by_secret["apiA"].get("secret", {}).get("ref") == "env:MA_E2E_TOKEN_A", f"endpoint A secret ref mismatch: {brokers}"
assert by_secret["apiB"].get("secret", {}).get("ref") == "env:MA_E2E_TOKEN_B", f"endpoint B secret ref mismatch: {brokers}"
assert by_secret["apiA"].get("assurance") == "trusted-upstream", f"endpoint A assurance mismatch: {brokers}"
assert by_secret["apiB"].get("assurance") == "trusted-upstream", f"endpoint B assurance mismatch: {brokers}"
manifest_text = json.dumps(manifest)
assert live_a not in manifest_text, "INVARIANT VIOLATION: endpoint A live secret persisted in the manifest"
assert live_b not in manifest_text, "INVARIANT VIOLATION: endpoint B live secret persisted in the manifest"

# The hard invariant, companion-wide: neither live secret is present in any
# regular file under the workspace's state. Non-regular entries (the serial
# FIFO, vsock/API sockets) are skipped by stat, not by open(2) — opening a
# FIFO would block forever waiting for a writer.
for root, _, files in os.walk(ws_state_dir):
    for fname in files:
        path = os.path.join(root, fname)
        try:
            if not stat.S_ISREG(os.lstat(path).st_mode):
                continue
            with open(path, "rb") as f:
                data = f.read()
        except OSError:
            continue
        assert live_a.encode() not in data, f"INVARIANT VIOLATION: endpoint A live secret present in {path}"
        assert live_b.encode() not in data, f"INVARIANT VIOLATION: endpoint B live secret present in {path}"

print("broker-multi: one guest with no network reached TWO upstreams through TWO broker "
      "endpoints, each injecting its own credential host-side; both hosts' decisions are "
      "in the shared trail and live via the egress view; both live credentials are absent "
      "from every file in the workspace state")
PY

echo "microagent E2E broker-multi passed"
