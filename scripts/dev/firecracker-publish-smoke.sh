#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-firecracker-publish.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
WORKSPACE="publish-smoke"
IMAGE="docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f"
EXPECTED_KERNEL_SHA="4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0"

cleanup() {
  status="$?"
  if [ "$status" -eq 0 ] && [ -x "$CLI" ]; then
    "$CLI" stop "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" delete "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_FIRECRACKER_PUBLISH_SMOKE:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept firecracker publish smoke state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    e2e_skip "firecracker publish smoke requires Linux amd64"
    ;;
esac

if [ ! -e /dev/kvm ]; then
  e2e_skip "/dev/kvm is not visible; run this smoke outside sandboxed environments"
fi

if [ -n "${MICROAGENT_FIRECRACKER:-}" ]; then
  firecracker="$MICROAGENT_FIRECRACKER"
elif command -v firecracker >/dev/null 2>&1; then
  firecracker="$(command -v firecracker)"
elif command -v brew >/dev/null 2>&1; then
  formula_prefix="$(brew --prefix microagent 2>/dev/null || true)"
  firecracker="$formula_prefix/libexec/firecracker"
else
  firecracker=""
fi

if [ ! -x "${firecracker:-}" ]; then
  e2e_skip "firecracker binary not found; install microagent or set MICROAGENT_FIRECRACKER"
fi

host_port="$(python3 - <<'PY'
import socket

with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
    s.bind(("127.0.0.1", 0))
    print(s.getsockname()[1])
PY
)"

export GOCACHE="$STATE_DIR/gocache"
export GOMODCACHE="$STATE_DIR/gomodcache"
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
kernel_path="$(python3 - "$STATE_DIR/kernel-install.json" "$EXPECTED_KERNEL_SHA" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)
if result.get("sha256") != sys.argv[2]:
    raise SystemExit(result)
print(result["path"])
PY
)"

"$CLI" create "$WORKSPACE" \
  --image "$IMAGE" \
  --arch amd64 \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR" \
  --size-mib 128 \
  --result-port 0 \
  --publish "127.0.0.1:${host_port}:8080/tcp" >"$STATE_DIR/create.json"

"$CLI" start "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --kernel "$kernel_path" >"$STATE_DIR/start.json"

"$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "printf PUBLISH_READY | nc -l -p 8080 &" \
  --timeout 2 >"$STATE_DIR/connect.txt"

python3 - "$host_port" "$STATE_DIR/curl.txt" <<'PY'
import socket
import sys
import time

port = int(sys.argv[1])
out = sys.argv[2]
deadline = time.time() + 20
last_error = ""
body = b""
while time.time() < deadline:
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=2) as sock:
            sock.settimeout(2)
            chunks = []
            while True:
                try:
                    chunk = sock.recv(4096)
                except TimeoutError:
                    break
                if not chunk:
                    break
                chunks.append(chunk)
                if b"PUBLISH_READY" in b"".join(chunks):
                    break
            body = b"".join(chunks)
            if b"PUBLISH_READY" in body:
                with open(out, "wb") as f:
                    f.write(body)
                raise SystemExit(0)
            last_error = body.decode("utf-8", errors="replace")
    except OSError as err:
        last_error = str(err)
    time.sleep(0.2)
with open(out, "wb") as f:
    f.write(body)
raise SystemExit(f"published TCP endpoint did not return PUBLISH_READY: {last_error}")
PY

"$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "killall nc 2>/dev/null || true; mkdir -p /www; printf HTTP_READY > /www/index.html; httpd -p 127.0.0.1:8080 -h /www" \
  --timeout 2 >"$STATE_DIR/http-connect.txt"

python3 - "$host_port" "$STATE_DIR/http.txt" <<'PY'
import socket
import sys
import time

port = int(sys.argv[1])
out = sys.argv[2]
deadline = time.time() + 20
last_error = ""
body = b""
while time.time() < deadline:
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=2) as sock:
            sock.settimeout(2)
            sock.sendall(b"GET / HTTP/1.0\r\nHost: 127.0.0.1\r\n\r\n")
            chunks = []
            while True:
                try:
                    chunk = sock.recv(4096)
                except TimeoutError:
                    break
                if not chunk:
                    break
                chunks.append(chunk)
                if b"HTTP_READY" in b"".join(chunks):
                    break
            body = b"".join(chunks)
            if b"HTTP_READY" in body:
                with open(out, "wb") as f:
                    f.write(body)
                raise SystemExit(0)
            last_error = body.decode("utf-8", errors="replace")
    except OSError as err:
        last_error = str(err)
    time.sleep(0.2)
with open(out, "wb") as f:
    f.write(body)
raise SystemExit(f"published HTTP endpoint did not return HTTP_READY: {last_error}")
PY

python3 - "$STATE_DIR/create.json" "$STATE_DIR/start.json" "$STATE_DIR/curl.txt" "$STATE_DIR/http.txt" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    create = json.load(f)
with open(sys.argv[2], "r", encoding="utf-8") as f:
    start = json.load(f)
with open(sys.argv[3], "r", encoding="utf-8", errors="replace") as f:
    tcp_body = f.read()
with open(sys.argv[4], "r", encoding="utf-8", errors="replace") as f:
    http_body = f.read()

if create["network"]["port_forwards"][0]["guestPort"] != 8080:
    raise SystemExit(create["network"])
if start["response"]["event"]["state"] != "running":
    raise SystemExit(start)
if "PUBLISH_READY" not in tcp_body:
    raise SystemExit(tcp_body)
if "HTTP_READY" not in http_body:
    raise SystemExit(http_body)
PY

"$CLI" stop "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/stop.json"
"$CLI" delete "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/delete.json"

echo "firecracker publish smoke passed"
