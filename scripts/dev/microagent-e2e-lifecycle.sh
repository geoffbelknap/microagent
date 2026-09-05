#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

default_backend() {
  case "$(uname -s):$(uname -m)" in
    Linux:x86_64|Linux:amd64)
      printf '%s\n' linux-kvm
      ;;
    Darwin:arm64)
      printf '%s\n' apple-vf
      ;;
    *)
      printf '%s\n' unsupported
      ;;
  esac
}

BACKEND="$(e2e_normalize_backend "${MICROAGENT_E2E_BACKEND:-$(default_backend)}")"

if [ "$BACKEND" = "linux-kvm" ]; then
  exec "$ROOT/scripts/dev/microagent-e2e-lifecycle-matrix.sh"
fi

if [ "$BACKEND" != "apple-vf" ]; then
  e2e_skip "microagent lifecycle E2E does not support backend lane: $BACKEND"
fi

case "$(uname -s):$(uname -m)" in
  Darwin:arm64)
    ;;
  *)
    e2e_skip "Apple VF lifecycle E2E requires macOS on Apple silicon"
    ;;
esac

SUPERVISOR="${MICROAGENT_APPLEVF_SUPERVISOR:-$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor}"
KERNEL="${MICROAGENT_APPLEVF_KERNEL:-$HOME/.microagent/kernels/apple-vf/arm64/Image}"
if [ ! -r "$KERNEL" ] && [ -r "$HOME/.microagent/kernels/apple-vf/Image" ]; then
  KERNEL="$HOME/.microagent/kernels/apple-vf/Image"
fi
IMAGE="${MICROAGENT_APPLEVF_BOOT_IMAGE:-docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6}"
ARCH="${MICROAGENT_APPLEVF_BOOT_ARCH:-arm64}"
# macOS's default TMPDIR lives under a long /var/folders path. Broker
# companions use AF_UNIX sockets (104-byte sun_path), so the maintained live
# fixture defaults to the short system temp alias while remaining overrideable.
STATE_DIR="$(mktemp -d "${MICROAGENT_APPLEVF_E2E_TMPDIR:-/tmp}/microagent-e2e-lifecycle-applevf.XXXXXX")"
WORKSPACE="lifecycle-e2e"
CLONE="lifecycle-clone"
FORCE_DELETE_WORKSPACE="lifecycle-force-delete"
CLI="$STATE_DIR/microagent"
GUEST_INIT="$STATE_DIR/microagent-guestinit"
ARTIFACT_DIR="$STATE_DIR/artifacts"
UPSTREAM_PID=""
LIVE_BROKER_SECRET="containment-broker-secret-$(date +%s)-$$"

cleanup() {
  status="$?"
  if [ -x "$CLI" ] && [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_LIFECYCLE:-0}" != "1" ]; then
    "$CLI" stop "$WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" stop "$CLONE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" kill "$FORCE_DELETE_WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" --reason "lifecycle E2E cleanup" --yes >/dev/null 2>&1 || true
    "$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" delete "$CLONE" --yes --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    "$CLI" delete "$FORCE_DELETE_WORKSPACE" --force --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
  fi
  if [ -n "$UPSTREAM_PID" ]; then
    kill "$UPSTREAM_PID" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_LIFECYCLE:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E lifecycle Apple VF state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

if [ ! -r "$KERNEL" ]; then
  e2e_skip "kernel is not readable at $KERNEL"
fi
if [ ! -x "$SUPERVISOR" ]; then
  e2e_skip "supervisor is not executable at $SUPERVISOR; run scripts/dev/applevf-supervisor-build.sh"
fi
export MICROAGENT_APPLEVF_SUPERVISOR="$SUPERVISOR"

if command -v mke2fs >/dev/null 2>&1; then
  MKE2FS="$(command -v mke2fs)"
elif [ -x /opt/homebrew/opt/e2fsprogs/sbin/mke2fs ]; then
  MKE2FS="/opt/homebrew/opt/e2fsprogs/sbin/mke2fs"
else
  e2e_skip "mke2fs not found; install e2fsprogs"
fi

if command -v debugfs >/dev/null 2>&1; then
  DEBUGFS="$(command -v debugfs)"
elif [ -x /opt/homebrew/opt/e2fsprogs/sbin/debugfs ]; then
  DEBUGFS="/opt/homebrew/opt/e2fsprogs/sbin/debugfs"
else
  e2e_skip "debugfs not found; install e2fsprogs"
fi

wait_for_status_ready() {
  local workspace="$1"
  local output="$2"
  local deadline="$((SECONDS + 45))"
  while true; do
    "$CLI" status "$workspace" --state-dir "$STATE_DIR" >"$output"
    if python3 - "$output" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    status = json.load(handle)
event = status.get("event") or {}
readiness = status.get("readiness") or {}
if event.get("state") == "running" and readiness.get("guestReady", {}).get("ready") and readiness.get("shellReady", {}).get("ready"):
    raise SystemExit(0)
raise SystemExit(1)
PY
    then
      return 0
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "workspace $workspace did not become ready" >&2
      cat "$output" >&2
      return 1
    fi
    sleep 1
  done
}

expect_failure() {
  name="$1"
  expected="$2"
  shift 2
  if "$@" >"$STATE_DIR/${name}.out" 2>"$STATE_DIR/${name}.err"; then
    echo "$name unexpectedly succeeded" >&2
    exit 1
  fi
  if ! grep -qi "$expected" "$STATE_DIR/${name}.err" && ! grep -qi "$expected" "$STATE_DIR/${name}.out"; then
    echo "$name failed without expected message: $expected" >&2
    cat "$STATE_DIR/${name}.out" >&2
    cat "$STATE_DIR/${name}.err" >&2
    exit 1
  fi
}

serial_count() {
  local pattern="$1"
  if [ ! -f "$STATE_DIR/$WORKSPACE/serial.log" ]; then
    printf '0\n'
    return
  fi
  tr -d '\r' <"$STATE_DIR/$WORKSPACE/serial.log" | grep -Ec "$pattern" || true
}

wait_for_containment_action() {
  local expected_count="${1:-1}"
  local deadline="$((SECONDS + 15))"
  while [ "$(serial_count '^CONTAINMENT-ACTION-[0-9]+$')" -lt "$expected_count" ]; do
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "continuous containment workload emitted no host-visible action" >&2
      exit 1
    fi
    sleep 0.1
  done
}

wait_for_broker_hit() {
  local deadline="$((SECONDS + 30))"
  while [ ! -s "$STATE_DIR/upstream.hits" ]; do
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "containment broker made no authenticated request to its live upstream" >&2
      tr -d '\r' <"$STATE_DIR/$WORKSPACE/serial.log" | grep -E '^CONTAINMENT-BROKER-' | tail -n 5 >&2 || true
      exit 1
    fi
    sleep 0.1
  done
}

wait_for_live_network() {
  local expected_count="${1:-1}"
  local deadline="$((SECONDS + 20))"
  while [ "$(serial_count '^CONTAINMENT-NETWORK-LIVE$')" -lt "$expected_count" ]; do
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "containment workspace did not exercise its live mediated network" >&2
      exit 1
    fi
    sleep 0.1
  done
}

assert_published_port() {
  local expected="$1"
  python3 - "$PUBLISHED_PORT" "$expected" <<'PY'
import socket
import sys
import time

port = int(sys.argv[1])
expected = sys.argv[2]
deadline = time.time() + 20
last_error = ""
while time.time() < deadline:
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=1) as sock:
            sock.settimeout(1)
            sock.sendall(b"GET / HTTP/1.0\r\nHost: 127.0.0.1\r\n\r\n")
            body = b""
            while expected.encode() not in body:
                chunk = sock.recv(4096)
                if not chunk:
                    break
                body += chunk
            if expected.encode() in body:
                raise SystemExit(0)
            last_error = body.decode("utf-8", errors="replace")
    except OSError as err:
        last_error = str(err)
    time.sleep(0.2)
raise SystemExit(f"published endpoint did not return {expected}: {last_error}")
PY
}

assert_published_port_closed() {
  python3 - "$PUBLISHED_PORT" <<'PY'
import socket
import sys
import time

port = int(sys.argv[1])
deadline = time.time() + 5
while time.time() < deadline:
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=0.5):
            time.sleep(0.1)
    except OSError:
        raise SystemExit(0)
raise SystemExit("published TCP listener stayed open after containment")
PY
}

assert_broker_socket_closed() {
  python3 - "$BROKER_SOCKET" <<'PY'
import os
import socket
import sys

path = sys.argv[1]
if not os.path.exists(path):
    raise SystemExit(0)
with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
    sock.settimeout(1)
    try:
        sock.connect(path)
    except OSError:
        raise SystemExit(0)
raise SystemExit(f"broker companion socket still accepted connections after containment: {path}")
PY
}

quarantine_and_assert_action_cutoff() {
  "$CLI" quarantine "$WORKSPACE" --state-dir "$STATE_DIR" --reason "lifecycle E2E quarantine" --yes >"$STATE_DIR/quarantine.json" &
  local quarantine_pid="$!"
  local deadline="$((SECONDS + 10))"
  while [ ! -d "$STATE_DIR/$WORKSPACE/containment" ]; do
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "containment was not accepted before timeout" >&2
      wait "$quarantine_pid" || true
      exit 1
    fi
    sleep 0.02
  done
  local accepted_count final_count stable_count
  accepted_count="$(serial_count '^CONTAINMENT-ACTION-[0-9]+$')"
  wait "$quarantine_pid"
  final_count="$(serial_count '^CONTAINMENT-ACTION-[0-9]+$')"
  sleep 2
  stable_count="$(serial_count '^CONTAINMENT-ACTION-[0-9]+$')"
  if [ "$accepted_count" -ne "$final_count" ] || [ "$final_count" -ne "$stable_count" ]; then
    echo "host-visible workload actions crossed after containment acceptance: accepted=$accepted_count final=$final_count stable=$stable_count" >&2
    exit 1
  fi
}

PUBLISHED_PORT="$(python3 - <<'PY'
import socket
with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)"

cat >"$STATE_DIR/upstream.py" <<'PY'
import os
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

expected = "Bearer " + os.environ["MICROAGENT_CONTAINMENT_TOKEN"]
hits_path, port_path = sys.argv[1:3]

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.headers.get("Authorization") == expected:
            with open(hits_path, "a", encoding="utf-8") as handle:
                handle.write("authenticated\n")
            body = b"BROKER_LIVE"
            self.send_response(200)
        else:
            body = b"DENIED"
            self.send_response(401)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_args):
        pass

server = HTTPServer(("127.0.0.1", 0), Handler)
with open(port_path, "w", encoding="utf-8") as handle:
    handle.write(str(server.server_address[1]))
server.serve_forever()
PY
export MICROAGENT_CONTAINMENT_TOKEN="$LIVE_BROKER_SECRET"
python3 "$STATE_DIR/upstream.py" "$STATE_DIR/upstream.hits" "$STATE_DIR/upstream.port" &
UPSTREAM_PID="$!"
for _ in $(seq 1 50); do
  [ -s "$STATE_DIR/upstream.port" ] && break
  sleep 0.1
done
[ -s "$STATE_DIR/upstream.port" ] || { echo "containment broker upstream did not start" >&2; exit 1; }
UPSTREAM_PORT="$(cat "$STATE_DIR/upstream.port")"

(
  cd "$ROOT"
  go build -buildvcs=false -o "$CLI" ./cmd/microagent
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

"$CLI" doctor --backend apple-vf --arch "$ARCH" --supervisor "$SUPERVISOR" >"$STATE_DIR/doctor.json"
"$CLI" profiles >"$STATE_DIR/profiles.json"

expect_failure invalid-restart "restart policy" \
  "$CLI" create invalid-restart --image "$IMAGE" --restart sometimes --state-dir "$STATE_DIR" --backend apple-vf --supervisor "$SUPERVISOR"
expect_failure invalid-network "network.mode" \
  "$CLI" create invalid-network --image "$IMAGE" --network made-up --state-dir "$STATE_DIR" --backend apple-vf --supervisor "$SUPERVISOR"
expect_failure invalid-publish "publish" \
  "$CLI" create invalid-publish --image "$IMAGE" --publish bad-mapping --state-dir "$STATE_DIR" --backend apple-vf --supervisor "$SUPERVISOR"
expect_failure reserved-disk "rootfs is reserved" \
  "$CLI" create invalid-disk --image "$IMAGE" --disk rootfs=/tmp/nope:/data:rw --state-dir "$STATE_DIR" --backend apple-vf --supervisor "$SUPERVISOR"
expect_failure mutable-rootfs "mutable" \
  "$CLI" rootfs build --image docker.io/library/busybox:1.36 --out "$STATE_DIR/mutable.ext4" --state-dir "$STATE_DIR/mutable-rootfs" --arch "$ARCH" --init "$GUEST_INIT" --mke2fs "$MKE2FS"

"$CLI" image pull "$IMAGE" \
  --state-dir "$STATE_DIR" \
  --arch "$ARCH" \
  --guest-init "$GUEST_INIT" \
  --mke2fs "$MKE2FS" \
  --size-mib "${MICROAGENT_APPLEVF_BOOT_SIZE_MIB:-128}" >"$STATE_DIR/images-pull.json"
"$CLI" image tag "$IMAGE" local/busybox-feature:probe --state-dir "$STATE_DIR" >"$STATE_DIR/images-tag.json"
"$CLI" image list --state-dir "$STATE_DIR" >"$STATE_DIR/images-list.json"

mkdir -p "$STATE_DIR/spec"
printf "seed-from-spec\n" >"$STATE_DIR/spec/seed.txt"
cat >"$STATE_DIR/spec/microagent.yaml" <<YAML
name: $WORKSPACE
image: $IMAGE
profile: tiny
restart: never
setup:
  - mkdir -p /matrix
  - printf setup-ok > /matrix/setup.txt
files:
  - src: ./seed.txt
    dst: /seed.txt
    mode: "0644"
env:
  MATRIX_ENV: env-ok
service: |
  mkdir -p /www
  printf PUBLISHED_LIVE > /www/index.html
  httpd -p 8080 -h /www
  if wget -qO- -T 10 http://1.1.1.1 >/dev/null; then
    echo "CONTAINMENT-NETWORK-LIVE" > /dev/console
  else
    echo "CONTAINMENT-NETWORK-FAILED" > /dev/console
  fi
  i=0
  while :; do
    echo "CONTAINMENT-ACTION-\$i" > /dev/console
    if [ -z "\${CONTAINMENT_BROKER_URL:-}" ]; then
      echo "CONTAINMENT-BROKER-\$i-URL-MISSING" > /dev/console
    elif response="\$(wget -O- -T 10 --header 'Authorization: Bearer @secret:containment' "\$CONTAINMENT_BROKER_URL/check" 2>&1)"; then
      echo "CONTAINMENT-BROKER-\$i-\$response" > /dev/console
    else
      broker_status="\$?"
      echo "CONTAINMENT-BROKER-\$i-ERROR-\$broker_status-\$response" > /dev/console
    fi
    i=\$((i + 1))
    sleep 5
  done
resources:
  memoryMiB: 512
  cpuCount: 2
  sizeMiB: 128
network:
  mode: user
  forwards:
    - protocol: tcp
      host: 127.0.0.1
      hostPort: $PUBLISHED_PORT
      guestPort: 8080
agent:
  egress: broker
  broker:
    upstream: http://127.0.0.1:$UPSTREAM_PORT
    secret: containment=env:MICROAGENT_CONTAINMENT_TOKEN
    assurance: trusted-upstream
    env: [CONTAINMENT_BROKER_URL]
outputs:
  - name: report
    path: /matrix/report.json
YAML

(
  cd "$STATE_DIR/spec"
  "$CLI" create --file microagent.yaml \
    --backend apple-vf \
    --state-dir "$STATE_DIR" \
    --mke2fs "$MKE2FS" \
    --kernel "$KERNEL" \
    --guest-init "$GUEST_INIT" \
    --supervisor "$SUPERVISOR" >"$STATE_DIR/create-spec.json"
)

"$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-prepared.json"
"$CLI" list --state-dir "$STATE_DIR" >"$STATE_DIR/ps-prepared.json"
"$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$KERNEL" --supervisor "$SUPERVISOR" >"$STATE_DIR/start.json"
wait_for_status_ready "$WORKSPACE" "$STATE_DIR/status-running.json"
"$CLI" list --state-dir "$STATE_DIR" >"$STATE_DIR/ps-running.json"
(
  printf 'echo INTERACTIVE_OK\n'
  sleep 1
  printf '\035'
) | "$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --ready-timeout 30 >"$STATE_DIR/connect-interactive.txt"
"$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "cat /seed.txt; cat /matrix/setup.txt; printf env=%s \"\$MATRIX_ENV\"; printf persisted > /matrix/persist.txt; printf '{\"ok\":true,\"phase\":\"running\"}' > /matrix/report.json; sync" \
  --ready-timeout 30 \
  --timeout 10 >"$STATE_DIR/connect-running.txt"
"$CLI" artifact "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/artifacts-running.json"
"$CLI" logs "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/logs-running.txt"
"$CLI" --json events "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/events-running.json"
"$CLI" --json stats "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/stats-running.json"
"$CLI" connect "$WORKSPACE" --state-dir "$STATE_DIR" \
  --send "printf halt-sync-survived > /matrix/halt-sync.txt" \
  --ready-timeout 30 --timeout 10 >"$STATE_DIR/connect-before-halt.txt"
"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/halt.json"
"$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-halted.json"
"$CLI" --json events "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/events-halted.json"

mkdir -p "$ARTIFACT_DIR/running" "$STATE_DIR/cp-out"
expect_failure unknown-artifact "not declared" \
  "$CLI" artifact get "$WORKSPACE" no-such "$STATE_DIR/no-artifact" --state-dir "$STATE_DIR" --debugfs "$DEBUGFS"
"$CLI" artifact get "$WORKSPACE" report "$ARTIFACT_DIR/running" \
  --state-dir "$STATE_DIR" \
  --debugfs "$DEBUGFS" >"$STATE_DIR/artifact-running.json"
printf "host-copied\n" >"$STATE_DIR/host-copy.txt"
"$CLI" cp "$STATE_DIR/host-copy.txt" "$WORKSPACE:/matrix/host-copy.txt" \
  --state-dir "$STATE_DIR" \
  --debugfs "$DEBUGFS" >"$STATE_DIR/cp-to-workspace.json"
"$CLI" cp "$WORKSPACE:/matrix/persist.txt" "$STATE_DIR/cp-out" \
  --state-dir "$STATE_DIR" \
  --debugfs "$DEBUGFS" >"$STATE_DIR/cp-from-workspace.json"
"$CLI" clone "$WORKSPACE" "$CLONE" --state-dir "$STATE_DIR" >"$STATE_DIR/clone.json"
"$CLI" list --state-dir "$STATE_DIR" >"$STATE_DIR/ps-cloned.json"

"$CLI" start "$CLONE" --state-dir "$STATE_DIR" --kernel "$KERNEL" --supervisor "$SUPERVISOR" >"$STATE_DIR/clone-start.json"
wait_for_status_ready "$CLONE" "$STATE_DIR/clone-status-running.json"
"$CLI" connect "$CLONE" \
  --state-dir "$STATE_DIR" \
  --send "cat /matrix/persist.txt; cat /matrix/host-copy.txt; printf '{\"ok\":true,\"phase\":\"clone\"}' > /matrix/report.json; sync" \
  --ready-timeout 30 \
  --timeout 10 >"$STATE_DIR/clone-connect.txt"
"$CLI" halt "$CLONE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/clone-halt.json"
mkdir -p "$ARTIFACT_DIR/clone"
"$CLI" artifact get "$CLONE" report "$ARTIFACT_DIR/clone" \
  --state-dir "$STATE_DIR" \
  --debugfs "$DEBUGFS" >"$STATE_DIR/clone-artifact.json"

"$CLI" clone "$WORKSPACE" "$FORCE_DELETE_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/force-delete-clone.json"
"$CLI" start "$FORCE_DELETE_WORKSPACE" --state-dir "$STATE_DIR" --kernel "$KERNEL" --supervisor "$SUPERVISOR" >"$STATE_DIR/force-delete-start.json"
wait_for_status_ready "$FORCE_DELETE_WORKSPACE" "$STATE_DIR/force-delete-status-running.json"
"$CLI" delete "$FORCE_DELETE_WORKSPACE" --force --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/force-delete-running.json"
test ! -e "$STATE_DIR/workspaces/$FORCE_DELETE_WORKSPACE"

expect_failure connect-halted "console input is unavailable" \
  "$CLI" connect "$WORKSPACE" --state-dir "$STATE_DIR" --send "echo should-not-run"

: >"$STATE_DIR/upstream.hits"
"$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$KERNEL" --supervisor "$SUPERVISOR" >"$STATE_DIR/resume.json"
wait_for_status_ready "$WORKSPACE" "$STATE_DIR/status-resumed.json"
"$CLI" connect "$WORKSPACE" --state-dir "$STATE_DIR" \
  --send "cat /matrix/halt-sync.txt" --ready-timeout 30 --timeout 10 >"$STATE_DIR/connect-after-halt.txt"
expect_failure start-running "already running" \
  "$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$KERNEL" --supervisor "$SUPERVISOR"
# A fresh Apple VF process recreates serial.log. Count only complete markers
# from this boot instead of carrying a pre-halt count across the restart.
wait_for_containment_action 1
wait_for_live_network 1
wait_for_broker_hit
assert_published_port PUBLISHED_LIVE
BROKER_VSOCK_PORT="$(python3 - "$STATE_DIR/$WORKSPACE/config.json" <<'PY'
import json
import sys
with open(sys.argv[1], "r", encoding="utf-8") as handle:
    config = json.load(handle)
brokers = config.get("brokers") or ([config["broker"]] if config.get("broker") else [])
if len(brokers) != 1 or not brokers[0].get("vsockPort"):
    raise SystemExit(config)
print(brokers[0]["vsockPort"])
PY
)"
BROKER_SOCKET="$STATE_DIR/$WORKSPACE/broker-$BROKER_VSOCK_PORT.sock"
[ -S "$BROKER_SOCKET" ] || { echo "live containment broker socket is missing: $BROKER_SOCKET" >&2; exit 1; }
quarantine_and_assert_action_cutoff
broker_hits_final="$(wc -l <"$STATE_DIR/upstream.hits")"
sleep 2
broker_hits_stable="$(wc -l <"$STATE_DIR/upstream.hits")"
if [ "$broker_hits_final" -ne "$broker_hits_stable" ]; then
  echo "broker requests crossed containment: final=$broker_hits_final stable=$broker_hits_stable" >&2
  exit 1
fi
assert_published_port_closed
assert_broker_socket_closed
expect_failure connect-quarantined "quarantined" \
  "$CLI" connect "$WORKSPACE" --state-dir "$STATE_DIR" --send "echo no"
expect_failure start-contained "containment marker" \
  "$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$KERNEL" --supervisor "$SUPERVISOR"
expect_failure resume-contained "containment marker" \
  "$CLI" resume "$WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR"
CAPTURE_TAG="$(python3 - "$STATE_DIR/quarantine.json" <<'PY'
import json
import sys
with open(sys.argv[1], "r", encoding="utf-8") as handle:
    print(json.load(handle)["captureTag"])
PY
)"
expect_failure restore-contained "containment marker" \
  "$CLI" start "$WORKSPACE" --from-snapshot "$CAPTURE_TAG" --state-dir "$STATE_DIR" --kernel "$KERNEL" --supervisor "$SUPERVISOR"
expect_failure halt-contained "containment marker" \
  "$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR"
expect_failure delete-contained-evidence "custody" \
  "$CLI" snapshot delete "$WORKSPACE" "$CAPTURE_TAG" --state-dir "$STATE_DIR"
cp "$STATE_DIR/$WORKSPACE/events.json" "$STATE_DIR/events.json"

"$CLI" delete "$CLONE" --yes --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/delete-clone.json"
expect_failure delete-contained "custody" \
  "$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR"
"$CLI" image delete local/busybox-feature:probe --state-dir "$STATE_DIR" >"$STATE_DIR/images-rm-tag.json"
"$CLI" image tag "$IMAGE" local/busybox-feature:delete-probe --state-dir "$STATE_DIR" >"$STATE_DIR/images-tag-delete.json"
"$CLI" image delete local/busybox-feature:delete-probe --purge --yes --state-dir "$STATE_DIR" >"$STATE_DIR/images-rm-delete.json"
"$CLI" image prune --state-dir "$STATE_DIR" >"$STATE_DIR/images-prune.json"
"$CLI" image prune --purge --yes --state-dir "$STATE_DIR" >"$STATE_DIR/prune-images-yes.txt"
"$CLI" image prune --purge --yes --state-dir "$STATE_DIR" >"$STATE_DIR/images-prune-delete.json"

python3 - "$STATE_DIR" "$WORKSPACE" "$CLONE" "$FORCE_DELETE_WORKSPACE" <<'PY'
import json
import os
import sys

state_dir, workspace, clone, force_delete = sys.argv[1:5]

def read_json(name):
    with open(os.path.join(state_dir, name), "r", encoding="utf-8") as handle:
        return json.load(handle)

def read_text(name):
    with open(os.path.join(state_dir, name), "r", encoding="utf-8", errors="replace") as handle:
        return handle.read()

doctor = read_json("doctor.json")
profiles = read_json("profiles.json")
pull = read_json("images-pull.json")
tag = read_json("images-tag.json")
images = read_json("images-list.json")
create = read_json("create-spec.json")
prepared = read_json("status-prepared.json")
running = read_json("status-running.json")
halted = read_json("status-halted.json")
events_halted = read_json("events-halted.json")
events_running = read_json("events-running.json")
stats_running = read_json("stats-running.json")
artifact = read_json("artifact-running.json")
copy_to = read_json("cp-to-workspace.json")
copy_from = read_json("cp-from-workspace.json")
clone_result = read_json("clone.json")
clone_running = read_json("clone-status-running.json")
clone_artifact = read_json("clone-artifact.json")
force_delete_clone = read_json("force-delete-clone.json")
force_delete_running = read_json("force-delete-status-running.json")
force_delete_result = read_json("force-delete-running.json")
resumed = read_json("status-resumed.json")
quarantine = read_json("quarantine.json")
delete_clone = read_json("delete-clone.json")
rm_delete = read_json("images-rm-delete.json")
prune_delete = read_json("images-prune-delete.json")
prune_images_yes = read_json("prune-images-yes.txt")

if doctor.get("ok") is not True or doctor.get("backend") != "apple-vf":
    raise SystemExit(doctor)
if not any(profile.get("name") == "tiny" for profile in profiles.get("profiles", [])):
    raise SystemExit(profiles)
if (pull.get("imageRef") or pull.get("image_ref", "")) == "" or (pull.get("outputPath") or pull.get("output_path", "")) == "":
    raise SystemExit(pull)
if (tag.get("imageRef") or tag.get("image_ref")) != "local/busybox-feature:probe":
    raise SystemExit(tag)
if not any((image.get("imageRef") or image.get("image_ref")) == "local/busybox-feature:probe" for image in images.get("images", [])):
    raise SystemExit(images)
if create.get("workspace") != workspace or create.get("response", {}).get("event", {}).get("state") not in ("prepared", "stopped"):
    raise SystemExit(create)
if create.get("profile") != "tiny" or create.get("restart") != "never":
    raise SystemExit(create)
if create.get("network", {}).get("mode") != "user":
    raise SystemExit(create)
create_forwards = create.get("network", {}).get("port_forwards") or create.get("network", {}).get("portForwards") or []
if len(create_forwards) != 1 or create_forwards[0].get("guestPort") != 8080:
    raise SystemExit(create.get("network"))
manifest_path = os.path.join(state_dir, "workspaces", workspace, "workspace.json")
with open(manifest_path, "r", encoding="utf-8") as handle:
    workspace_manifest = json.load(handle)
broker = workspace_manifest.get("broker") or {}
if broker.get("assurance") != "trusted-upstream" or broker.get("secret", {}).get("ref") != "env:MICROAGENT_CONTAINMENT_TOKEN":
    raise SystemExit(broker)
if prepared.get("event", {}).get("state") not in ("prepared", "stopped"):
    raise SystemExit(prepared)
if running.get("event", {}).get("state") != "running":
    raise SystemExit(running)
constraint_history = running.get("constraintHistory", {})
if constraint_history.get("count", 0) < 3 or constraint_history.get("maxEntries") != 1024:
    raise SystemExit(constraint_history)
latest_constraint = constraint_history.get("latest", {})
if latest_constraint.get("runtimeID") != workspace or not latest_constraint.get("manifestSHA256") or not latest_constraint.get("configDiskSHA256"):
    raise SystemExit(latest_constraint)
verification = running.get("verification", {})
if verification.get("ok") is False:
    if not any(item.get("artifact") == "rootfs" for item in verification.get("divergence", [])):
        raise SystemExit(running)
elif verification.get("ok") is not True:
    raise SystemExit(running)
if not running.get("readiness", {}).get("guestReady", {}).get("ready"):
    raise SystemExit(running)
if not running.get("readiness", {}).get("shellReady", {}).get("ready"):
    raise SystemExit(running)
connect = read_text("connect-running.txt")
for needle in ("seed-from-spec", "setup-ok", "env=env-ok"):
    if needle not in connect:
        raise SystemExit(connect)
if "INTERACTIVE_OK" not in read_text("connect-interactive.txt"):
    raise SystemExit(read_text("connect-interactive.txt"))
if "microagent-init: starting" not in read_text("logs-running.txt"):
    raise SystemExit("logs missing guest init output")
if events_running.get("workspace") != workspace or not events_running.get("events"):
    raise SystemExit(events_running)
if not any(event.get("state") == "running" for event in events_running.get("events", [])):
    raise SystemExit(events_running)
constraint_events = [event for event in events_running.get("events", []) if event.get("source") == "constraint"]
if not constraint_events or not any(event.get("raw", {}).get("manifest", {}).get("name") == workspace for event in constraint_events):
    raise SystemExit(events_running)
if stats_running.get("pid", 0) <= 0:
    raise SystemExit(stats_running)
if halted.get("event", {}).get("state") != "halted":
    raise SystemExit(halted)
if "halt-sync-survived" not in read_text("connect-after-halt.txt"):
    raise SystemExit("bounded halt sync did not preserve the final guest write")
if not any("guest filesystem sync completed" in event.get("detail", "") for event in events_halted.get("events", [])):
    raise SystemExit(events_halted)
if artifact.get("artifact") != "report" or artifact.get("disk") != "rootfs":
    raise SystemExit(artifact)
with open(os.path.join(state_dir, "artifacts", "running", "report.json"), "r", encoding="utf-8") as handle:
    if json.load(handle) != {"ok": True, "phase": "running"}:
        raise SystemExit("running artifact mismatch")
if copy_to.get("direction") != "to-workspace" or copy_from.get("direction") != "from-workspace":
    raise SystemExit((copy_to, copy_from))
with open(os.path.join(state_dir, "cp-out", "persist.txt"), "r", encoding="utf-8") as handle:
    if handle.read() != "persisted":
        raise SystemExit("copied persisted file mismatch")
if clone_result.get("workspace") != clone or clone_result.get("response", {}).get("event", {}).get("state") != "prepared":
    raise SystemExit(clone_result)
if clone_running.get("event", {}).get("state") != "running":
    raise SystemExit(clone_running)
clone_output = read_text("clone-connect.txt")
for needle in ("persisted", "host-copied"):
    if needle not in clone_output:
        raise SystemExit(f"clone console missing {needle!r}; clone-connect.txt = {clone_output!r}")
with open(os.path.join(state_dir, "artifacts", "clone", "report.json"), "r", encoding="utf-8") as handle:
    if json.load(handle) != {"ok": True, "phase": "clone"}:
        raise SystemExit("clone artifact mismatch")
if clone_artifact.get("artifact") != "report":
    raise SystemExit(clone_artifact)
if force_delete_clone.get("workspace") != force_delete or force_delete_clone.get("response", {}).get("event", {}).get("state") != "prepared":
    raise SystemExit(force_delete_clone)
if force_delete_running.get("event", {}).get("state") != "running":
    raise SystemExit(force_delete_running)
if force_delete_result.get("event", {}).get("state") != "stopped":
    raise SystemExit(force_delete_result)
if resumed.get("event", {}).get("state") != "running":
    raise SystemExit(resumed)
if quarantine.get("event", {}).get("state") != "quarantined":
    raise SystemExit(quarantine)
quarantine_audit = quarantine.get("event", {}).get("lifecycle", {})
if quarantine_audit.get("reason") != "lifecycle E2E quarantine":
    raise SystemExit(quarantine_audit)
if quarantine_audit.get("initiator", {}).get("channel") != "cli" or quarantine_audit.get("initiator", {}).get("assurance") != "unavailable":
    raise SystemExit(quarantine_audit)
quarantine_work = quarantine_audit.get("workInFlight", {})
if quarantine_work.get("captureStatus") != "frozen_forensic_capture":
    raise SystemExit(quarantine_work)
if not quarantine_work.get("evidenceRef", "").startswith("snapshot:forensic-"):
    raise SystemExit(quarantine_work)
if quarantine_audit.get("notification", {}).get("status") != "not_performed" or quarantine_audit.get("notification", {}).get("owner") != "caller":
    raise SystemExit(quarantine_audit)
containment = quarantine.get("containment") or {}
for phase in ("freeze", "severance", "capture", "stop", "custody"):
    if containment.get(phase, {}).get("status") != "completed":
        raise SystemExit(containment)
if containment.get("state") != "contained" or containment.get("captureTag") != quarantine.get("captureTag"):
    raise SystemExit(containment)
with open(os.path.join(state_dir, workspace, "quarantine.ack.json"), "r", encoding="utf-8") as handle:
    quarantine_ack = json.load(handle)
if quarantine_ack.get("networkDevicesDetached", 0) < 1:
    raise SystemExit(quarantine_ack)
if quarantine_ack.get("vsockListenersRemoved", 0) < 1:
    raise SystemExit(quarantine_ack)
if quarantine_ack.get("publishedPortsClosed") != 1:
    raise SystemExit(quarantine_ack)
if quarantine_ack.get("datapathPresent") is not True or quarantine_ack.get("datapathTerminated") is not True:
    raise SystemExit(quarantine_ack)
if quarantine_ack.get("brokerCompanionsPresent") != 1 or quarantine_ack.get("brokerCompanionsTerminated") != 1:
    raise SystemExit(quarantine_ack)
if quarantine_ack.get("serialInputRemoved") is not True or quarantine_ack.get("error"):
    raise SystemExit(quarantine_ack)
snapshot_dir = os.path.join(state_dir, workspace, "snapshots", quarantine.get("captureTag", ""))
with open(os.path.join(snapshot_dir, "manifest.json"), "r", encoding="utf-8") as handle:
    forensic_manifest = json.load(handle)
if not forensic_manifest.get("forensic") or not forensic_manifest.get("frozenProcessState"):
    raise SystemExit(forensic_manifest)
artifact_paths = [item.get("path") for item in forensic_manifest.get("machineStateArtifacts", [])]
for artifact in artifact_paths:
    path = os.path.join(snapshot_dir, artifact)
    if not os.path.isfile(path) or os.path.getsize(path) <= 0:
        raise SystemExit(f"missing frozen machine-state artifact: {path}")
if delete_clone.get("event", {}).get("state") != "stopped":
    raise SystemExit(delete_clone)
if "removed" not in rm_delete or "removed" not in prune_delete:
    raise SystemExit((rm_delete, prune_delete))
if "deleted" not in prune_images_yes or "kept" not in prune_images_yes:
    raise SystemExit(prune_images_yes)
with open(os.path.join(state_dir, "events.json"), "r", encoding="utf-8") as handle:
    states = [event["state"] for event in json.load(handle)]
for expected in ("running", "halted", "quarantined"):
    if expected not in states:
        raise SystemExit(states)
if states.count("running") < 2:
    raise SystemExit(states)
PY

echo "microagent E2E lifecycle passed for apple-vf"
