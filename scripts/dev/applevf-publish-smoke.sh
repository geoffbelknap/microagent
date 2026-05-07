#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SUPERVISOR="${MICROAGENT_APPLEVF_SUPERVISOR:-$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor}"
KERNEL="${MICROAGENT_APPLEVF_KERNEL:-$HOME/.microagent/kernels/apple-vf/arm64/Image}"
if [ ! -r "$KERNEL" ] && [ -r "$HOME/.microagent/kernels/apple-vf/Image" ]; then
  KERNEL="$HOME/.microagent/kernels/apple-vf/Image"
fi
IMAGE="${MICROAGENT_APPLEVF_BOOT_IMAGE:-docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6}"
ARCH="${MICROAGENT_APPLEVF_BOOT_ARCH:-arm64}"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-applevf-publish.XXXXXX")"
WORKSPACE="publish-smoke"
CLI="$STATE_DIR/microagent"
GUEST_INIT="$STATE_DIR/microagent-guestinit"

cleanup() {
  status="$?"
  if [ "$status" -eq 0 ] && [ -x "$CLI" ]; then
    "$CLI" stop "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" delete "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  fi
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_APPLEVF_PUBLISH_SMOKE:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept Apple VF publish smoke state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

if [ "$(uname -s)" != "Darwin" ] || [ "$(uname -m)" != "arm64" ]; then
  echo "Apple VF publish smoke requires macOS on Apple silicon" >&2
  exit 2
fi
if [ ! -r "$KERNEL" ]; then
  echo "kernel is not readable at $KERNEL" >&2
  exit 2
fi
if [ ! -x "$SUPERVISOR" ]; then
  echo "supervisor is not executable at $SUPERVISOR; run make signed-supervisor" >&2
  exit 2
fi

if command -v mke2fs >/dev/null 2>&1; then
  MKE2FS="$(command -v mke2fs)"
elif [ -x /opt/homebrew/opt/e2fsprogs/sbin/mke2fs ]; then
  MKE2FS="/opt/homebrew/opt/e2fsprogs/sbin/mke2fs"
else
  echo "mke2fs not found; install e2fsprogs" >&2
  exit 2
fi

host_port="$(python3 - <<'PY'
import socket

with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
    s.bind(("127.0.0.1", 0))
    print(s.getsockname()[1])
PY
)"

(
  cd "$ROOT"
  go build -o "$CLI" ./cmd/microagent
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

"$CLI" create "$WORKSPACE" \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --kernel "$KERNEL" \
  --guest-init "$GUEST_INIT" \
  --supervisor "$SUPERVISOR" \
  --state-dir "$STATE_DIR" \
  --size-mib "${MICROAGENT_APPLEVF_BOOT_SIZE_MIB:-128}" \
  --mke2fs "$MKE2FS" \
  --result-port 0 \
  --publish "127.0.0.1:${host_port}:8080/tcp" >"$STATE_DIR/create.json"

"$CLI" start "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" >"$STATE_DIR/start.json"

"$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "printf PUBLISH_READY | nc -l -p 8080 &" \
  --timeout 2 >"$STATE_DIR/connect.txt"

python3 - "$host_port" "$STATE_DIR/tcp.txt" <<'PY'
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

python3 - "$STATE_DIR/create.json" "$STATE_DIR/start.json" "$STATE_DIR/tcp.txt" "$STATE_DIR/http.txt" <<'PY'
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

echo "Apple VF publish smoke passed"
