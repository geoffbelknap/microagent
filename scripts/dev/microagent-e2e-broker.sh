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
#   - the broker access trail is the minimized decision stream (verdict +
#     metadata + reference NAMES — no path, headers, or bodies) and is live
#     via `microagent egress`;
#   - `--broker-capture` opts in to the governed raw capture: the pre-swap
#     request (reference verbatim, path, body) lands in an owner-only file;
#   - the live secret is provably absent from EVERY file in the workspace
#     state (absent by construction, not redaction).

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
    def _answer(self):
        length = int(self.headers.get("Content-Length") or 0)
        if length:
            self.rfile.read(length)
        if self.headers.get("Authorization") == expected:
            body = b"UPSTREAM-SAW-LIVE-SECRET"
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
GUEST_EXEC='resp="$(wget -qO- --post-data "guest-request-body" --header "Authorization: Bearer @secret:api" http://127.0.0.1:18888/check)" && echo "RESP:$resp"; echo GUEST-ENV-BEGIN; env; echo GUEST-ENV-END'
MA_E2E_BROKER_TOKEN="$LIVE_SECRET" "$CLI" --mode=ax run \
  --name "$WORKSPACE" \
  --image "$IMAGE" \
  --network isolated \
  --broker-upstream "http://127.0.0.1:$UPSTREAM_PORT" \
  --broker-secret "api=env:MA_E2E_BROKER_TOKEN" \
  --broker-capture \
  --keep \
  --exec "$GUEST_EXEC" \
  --state-dir "$STATE_DIR" >"$STATE_DIR/run.json"

# The merged live view surfaces the broker's decision records.
"$CLI" egress "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/egress-view.txt"

BROKER_TRAIL="$STATE_DIR/$WORKSPACE/broker-access.jsonl"
BROKER_CAPTURE="$STATE_DIR/$WORKSPACE/broker-capture.jsonl"
MA_E2E_BROKER_TOKEN="$LIVE_SECRET" python3 - \
  "$STATE_DIR/run.json" \
  "$BROKER_TRAIL" \
  "$STATE_DIR/workspaces/$WORKSPACE/workspace.json" \
  "$BROKER_CAPTURE" \
  "$STATE_DIR/$WORKSPACE" \
  "$STATE_DIR/egress-view.txt" <<'PY'
import base64, json, os, sys

run_path, trail_path, manifest_path, capture_path, ws_state_dir, view_path = sys.argv[1:7]
live = os.environ["MA_E2E_BROKER_TOKEN"]

with open(run_path) as f:
    envelope = json.load(f)
# AX responses wrap the body in one {ok, result} envelope; unwrap to the run result.
run = envelope.get("result") or {}
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

# The broker access trail is the minimized decision stream: verdict, metadata,
# and the NAMES of the references used — no path, no headers, no body, and
# never the live secret (absent by construction, not redaction).
with open(trail_path) as f:
    trail = f.read()
assert "broker_request_allow" in trail, f"decision record missing from broker trail:\n{trail}"
assert '"secret_refs":["api"]' in trail, f"credential-use metadata missing:\n{trail}"
for banned in ("@secret:api", "/check", '"headers"', "guest-request-body", live):
    assert banned not in trail, f"default trail must be minimized metadata, found {banned!r}:\n{trail}"

# The governed capture holds the pre-swap request: reference verbatim, path,
# body — and still never the live secret.
with open(capture_path) as f:
    capture = f.read()
assert "@secret:api" in capture, f"pre-swap reference missing from capture:\n{capture}"
assert "/check" in capture, f"path missing from capture:\n{capture}"
body_b64 = base64.b64encode(b"guest-request-body").decode()
assert body_b64 in capture, f"request body missing from capture:\n{capture}"
assert live not in capture, f"INVARIANT VIOLATION: live secret present in capture:\n{capture}"

# The merged `microagent egress` view surfaces the broker's decisions live.
with open(view_path) as f:
    view = f.read()
assert "broker_request_allow" in view, f"broker decisions missing from egress view:\n{view}"

# The manifest persists the broker config — reference only, capture opt-in
# declared — so every start re-arms the broker identically.
with open(manifest_path) as f:
    manifest = json.load(f)
broker = manifest.get("broker") or {}
assert broker.get("upstream", "").startswith("http://127.0.0.1:"), f"manifest broker = {broker}"
assert broker.get("secret", {}).get("ref") == "env:MA_E2E_BROKER_TOKEN", f"manifest broker secret = {broker.get('secret')}"
assert broker.get("capture") is True, f"capture opt-in not declared in manifest: {broker}"
assert live not in json.dumps(manifest), "INVARIANT VIOLATION: live secret persisted in the manifest"

# The hard invariant, companion-wide: the live secret is absent from EVERY
# regular file under the workspace's state. Non-regular entries (the serial
# FIFO, vsock/API sockets) are skipped by stat, not by open(2) — opening a
# FIFO would block forever waiting for a writer.
import stat
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
        assert live.encode() not in data, f"INVARIANT VIOLATION: live secret present in {path}"

print("broker: guest with no network reached the upstream through the broker; "
      "decision stream minimized + live via egress view; capture held the pre-swap "
      "request; live credential absent from every file in the workspace state")
PY

echo "microagent E2E broker passed"
