#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"
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
  if [ -x "$CLI" ]; then
    "$CLI" stop "$WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    if [ "$status" -eq 0 ]; then
      "$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    fi
  fi
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_APPLEVF_PUBLISH_SMOKE:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept Apple VF publish smoke state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

if [ "$(uname -s)" != "Darwin" ] || [ "$(uname -m)" != "arm64" ]; then
  e2e_skip "Apple VF publish smoke requires macOS on Apple silicon"
fi
if [ ! -r "$KERNEL" ]; then
  e2e_skip "kernel is not readable at $KERNEL"
fi
if [ ! -x "$SUPERVISOR" ]; then
  e2e_skip "supervisor is not executable at $SUPERVISOR; run make signed-supervisor"
fi

if command -v mke2fs >/dev/null 2>&1; then
  MKE2FS="$(command -v mke2fs)"
elif [ -x /opt/homebrew/opt/e2fsprogs/sbin/mke2fs ]; then
  MKE2FS="/opt/homebrew/opt/e2fsprogs/sbin/mke2fs"
else
  e2e_skip "mke2fs not found; install e2fsprogs"
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
  --network user \
  --egress broker \
  --service-command "mkdir -p /www; printf HTTP_READY > /www/index.html; exec httpd -f -p 8080 -h /www" \
  --publish "127.0.0.1:${host_port}:8080/tcp" >"$STATE_DIR/create.json"

"$CLI" start "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" >"$STATE_DIR/start.json"

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

python3 - "$STATE_DIR/create.json" "$STATE_DIR/start.json" "$STATE_DIR/http.txt" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    create = json.load(f)
with open(sys.argv[2], "r", encoding="utf-8") as f:
    start = json.load(f)
with open(sys.argv[3], "r", encoding="utf-8", errors="replace") as f:
    http_body = f.read()

if create["network"]["port_forwards"][0]["guestPort"] != 8080:
    raise SystemExit(create["network"])
if start["response"]["event"]["state"] != "running":
    raise SystemExit(start)
if "HTTP_READY" not in http_body:
    raise SystemExit(http_body)
PY

"$CLI" --json status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status.json"
"$CLI" --json network "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/network.json"
python3 - "$host_port" "$STATE_DIR/status.json" "$STATE_DIR/network.json" <<'PY'
import json
import sys

host_port = int(sys.argv[1])
with open(sys.argv[2], "r", encoding="utf-8") as f:
    status = json.load(f)
with open(sys.argv[3], "r", encoding="utf-8") as f:
    network = json.load(f)

for label, body in (("status", status), ("network", network)):
    cfg = body.get("network") or {}
    if cfg.get("mode") != "user":
        raise SystemExit(f"{label} mode: {cfg}")
    forwards = cfg.get("portForwards") or cfg.get("port_forwards") or []
    if not forwards:
        raise SystemExit(f"{label} missing port forwards: {cfg}")
    forward = forwards[0]
    if forward.get("hostPort") != host_port or forward.get("guestPort") != 8080:
        raise SystemExit(f"{label} forward mismatch: {forward}")
    runtime = cfg.get("runtime")
    if runtime is not None:
        runtime_forwards = runtime.get("portForwards") or []
        if not runtime_forwards or runtime_forwards[0].get("hostPort") != host_port:
            raise SystemExit(f"{label} runtime forward mismatch: {runtime}")
PY

"$CLI" quarantine "$WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" --reason "publish smoke quarantine" --yes >"$STATE_DIR/quarantine.json"
python3 - "$host_port" <<'PY'
import socket
import sys
import time

port = int(sys.argv[1])
deadline = time.time() + 5
last_error = ""
while time.time() < deadline:
    try:
        with socket.create_connection(("127.0.0.1", port), timeout=0.5):
            time.sleep(0.2)
            continue
    except OSError as err:
        last_error = str(err)
        break
else:
    raise SystemExit("published TCP listener stayed open after quarantine")
if not last_error:
    raise SystemExit("published TCP listener check did not observe closure")
PY

python3 - "$STATE_DIR/quarantine.json" "$STATE_DIR/$WORKSPACE/quarantine.ack.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as handle:
    quarantine = json.load(handle)
with open(sys.argv[2], "r", encoding="utf-8") as handle:
    ack = json.load(handle)
containment = quarantine.get("containment") or {}
for phase in ("freeze", "severance", "capture", "stop", "custody"):
    if containment.get(phase, {}).get("status") != "completed":
        raise SystemExit(containment)
if ack.get("networkDevicesDetached", 0) < 1 or ack.get("publishedPortsClosed") != 1:
    raise SystemExit(ack)
if ack.get("datapathPresent") is not True or ack.get("datapathTerminated") is not True or ack.get("error"):
    raise SystemExit(ack)
PY

echo "Apple VF publish smoke passed"
