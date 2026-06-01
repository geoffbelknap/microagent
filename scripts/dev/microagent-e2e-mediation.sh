#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-mediation.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
WORKSPACE="mediation-smoke"
OPTIONAL_WORKSPACE="mediation-optional"
IMAGE="${MICROAGENT_NATS_IMAGE:-docker.io/library/nats@sha256:6e0cca2c6da79f0a3542ec5a3319dd10b1b05f5d8e8949afa8e9cdf6314bbf6c}"
EXPECTED_KERNEL_SHA="4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0"
SERVER_PID=""
RAW_SERVER_PID=""
RAW_LARGE_SERVER_PID=""

cleanup() {
  status="$?"
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" >/dev/null 2>&1 || true
  fi
  if [ -n "$RAW_SERVER_PID" ]; then
    kill "$RAW_SERVER_PID" >/dev/null 2>&1 || true
  fi
  if [ -n "$RAW_LARGE_SERVER_PID" ]; then
    kill "$RAW_LARGE_SERVER_PID" >/dev/null 2>&1 || true
  fi
  if [ -x "$CLI" ]; then
    for workspace in "$WORKSPACE" "$OPTIONAL_WORKSPACE"; do
      "$CLI" stop "$workspace" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      "$CLI" delete "$workspace" --yes --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    done
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_MEDIATION:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E mediation state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    echo "microagent E2E mediation requires Linux amd64" >&2
    exit 2
    ;;
esac

if [ ! -e /dev/kvm ]; then
  echo "/dev/kvm is not visible; run this smoke outside sandboxed environments" >&2
  exit 2
fi

if [ ! -e /dev/vhost-vsock ]; then
  echo "/dev/vhost-vsock is not visible; mediation requires vsock" >&2
  exit 2
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
  echo "Linux microagent E2E requires the Firecracker backend binary; install firecracker on PATH or set MICROAGENT_FIRECRACKER" >&2
  exit 2
fi

export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
export GOFLAGS="${GOFLAGS:-} -modcacherw"
export MICROAGENT_FIRECRACKER="$firecracker"
export MICROAGENT_FIRECRACKER_SUPERVISOR="$SUPERVISOR"

wait_for_status_ready() {
  workspace="$1"
  output="$2"
  deadline="$((SECONDS + 45))"
  while true; do
    "$CLI" status "$workspace" --state-dir "$STATE_DIR" >"$output"
    if python3 - "$output" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    status = json.load(f)
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

(
  cd "$ROOT"
  go build -buildvcs=false -o "$CLI" ./cmd/microagent
  go build -buildvcs=false -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

"$CLI" kernel install --backend firecracker --arch amd64 >"$STATE_DIR/kernel-install.json"
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

python3 - "$STATE_DIR/host-port.txt" "$STATE_DIR/host-request.txt" <<'PY' &
import socket
import sys

port_file, request_file = sys.argv[1:3]
srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", 0))
srv.listen(1)
with open(port_file, "w", encoding="utf-8") as f:
    f.write(str(srv.getsockname()[1]))
    f.write("\n")
while True:
    conn, addr = srv.accept()
    with conn:
        conn.settimeout(10)
        data = bytearray()
        while b"\r\n\r\n" not in data:
            chunk = conn.recv(4096)
            if not chunk:
                break
            data.extend(chunk)
        if not data:
            continue
        with open(request_file, "wb") as f:
            f.write(bytes(data))
        body = b"MEDIATION_OK\n"
        conn.sendall(
            b"HTTP/1.1 200 OK\r\n"
            + b"Content-Type: text/plain\r\n"
            + b"Content-Length: "
            + str(len(body)).encode()
            + b"\r\nConnection: close\r\n\r\n"
            + body
        )
PY
SERVER_PID="$!"

python3 - "$STATE_DIR/raw-host-port.txt" "$STATE_DIR/raw-host-request.txt" <<'PY' &
import socket
import sys

port_file, request_file = sys.argv[1:3]
srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", 0))
srv.listen(1)
with open(port_file, "w", encoding="utf-8") as f:
    f.write(str(srv.getsockname()[1]))
    f.write("\n")
conn, addr = srv.accept()
with conn:
    conn.settimeout(10)
    data = bytearray()
    while b"\r\n\r\n" not in data:
        chunk = conn.recv(4096)
        if not chunk:
            break
        data.extend(chunk)
    with open(request_file, "wb") as f:
        f.write(bytes(data))
    body = b"RAW_VSOCK_OK\n"
    conn.sendall(
        b"HTTP/1.1 200 OK\r\n"
        + b"Content-Type: text/plain\r\n"
        + b"Content-Length: "
        + str(len(body)).encode()
        + b"\r\nConnection: close\r\n\r\n"
        + body
    )
srv.close()
PY
RAW_SERVER_PID="$!"

python3 - "$STATE_DIR/raw-large-host-port.txt" "$STATE_DIR/raw-large-host-request.txt" <<'PY' &
import socket
import sys

port_file, request_file = sys.argv[1:3]
srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", 0))
srv.listen(1)
with open(port_file, "w", encoding="utf-8") as f:
    f.write(str(srv.getsockname()[1]))
    f.write("\n")
conn, addr = srv.accept()
with conn:
    conn.settimeout(10)
    data = bytearray()
    while b"\r\n\r\n" not in data:
        chunk = conn.recv(4096)
        if not chunk:
            break
        data.extend(chunk)
    with open(request_file, "wb") as f:
        f.write(bytes(data))
    body = b"RAW_LARGE_BEGIN\n" + (b"A" * 16384) + b"\nRAW_LARGE_END\n"
    conn.sendall(
        b"HTTP/1.1 200 OK\r\n"
        + b"Content-Type: text/plain\r\n"
        + b"Content-Length: "
        + str(len(body)).encode()
        + b"\r\nConnection: close\r\n\r\n"
        + body
    )
srv.close()
PY
RAW_LARGE_SERVER_PID="$!"

for _ in $(seq 1 50); do
  if [ -s "$STATE_DIR/host-port.txt" ] && [ -s "$STATE_DIR/raw-host-port.txt" ] && [ -s "$STATE_DIR/raw-large-host-port.txt" ]; then
    break
  fi
  sleep 0.1
done
test -s "$STATE_DIR/host-port.txt"
test -s "$STATE_DIR/raw-host-port.txt"
test -s "$STATE_DIR/raw-large-host-port.txt"
host_port="$(cat "$STATE_DIR/host-port.txt")"
raw_host_port="$(cat "$STATE_DIR/raw-host-port.txt")"
raw_large_host_port="$(cat "$STATE_DIR/raw-large-host-port.txt")"
optional_host_port="$(python3 - <<'PY'
import socket

with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
    s.bind(("127.0.0.1", 0))
    print(s.getsockname()[1])
PY
)"
raw_unavailable_port="$(python3 - <<'PY'
import socket

with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
    s.bind(("127.0.0.1", 0))
    print(s.getsockname()[1])
PY
)"

"$CLI" doctor >"$STATE_DIR/doctor.json"
"$CLI" create "$WORKSPACE" \
  --image "$IMAGE" \
  --arch amd64 \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR" \
  --size-mib 192 \
  --result-port 1024 \
  --network isolated \
  --env MICROAGENT_VSOCK_TCP_LISTENERS=127.0.0.1:18080=2048,127.0.0.1:18081=2050,127.0.0.1:18082=2051,127.0.0.1:18083=2052 \
  --mediation "2048=127.0.0.1:${host_port}" >"$STATE_DIR/create.json"

"$CLI" start "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --kernel "$kernel_path" \
  --vsock "2050=127.0.0.1:${raw_host_port}" \
  --vsock "2051=127.0.0.1:${raw_large_host_port}" \
  --vsock "2052=127.0.0.1:${raw_unavailable_port}" >"$STATE_DIR/start.json"
wait_for_status_ready "$WORKSPACE" "$STATE_DIR/status-running.json"
"$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "wget -qO- -T 10 http://127.0.0.1:18080/mediation-check; wget -qO- -T 10 http://127.0.0.1:18081/raw-vsock-check; wget -qO- -T 10 http://127.0.0.1:18082/raw-large-check > /tmp/raw-large.out; cat /tmp/raw-large.out" \
  --ready-timeout 30 \
  --timeout 45 >"$STATE_DIR/connect.txt"
"$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "if wget -qO- -T 3 http://127.0.0.1:18083/raw-unavailable; then echo RAW_UNAVAILABLE_UNEXPECTED; else echo RAW_UNAVAILABLE_FAILED; fi" \
  --ready-timeout 30 \
  --timeout 10 >"$STATE_DIR/connect-unavailable.txt"
wait "$RAW_SERVER_PID"
RAW_SERVER_PID=""
wait "$RAW_LARGE_SERVER_PID"
RAW_LARGE_SERVER_PID=""
"$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-after-connect.json"
cp "$STATE_DIR/$WORKSPACE/runtime.json" "$STATE_DIR/runtime-after-connect.json"
"$CLI" quarantine "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/quarantine.json"
if "$CLI" connect "$WORKSPACE" --state-dir "$STATE_DIR" --send "echo no" >"$STATE_DIR/connect-quarantined.txt" 2>"$STATE_DIR/connect-quarantined.err"; then
  echo "connect succeeded after mediation workspace was quarantined" >&2
  exit 1
fi
grep -qi "quarantined" "$STATE_DIR/connect-quarantined.err"
"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt.json"
"$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete.json"

"$CLI" create "$OPTIONAL_WORKSPACE" \
  --image "$IMAGE" \
  --arch amd64 \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR" \
  --size-mib 192 \
  --result-port 1024 \
  --network isolated \
  --mediation "2049=127.0.0.1:${optional_host_port}" \
  --mediation-optional >"$STATE_DIR/create-optional.json"

"$CLI" start "$OPTIONAL_WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --kernel "$kernel_path" >"$STATE_DIR/start-optional.json"
wait_for_status_ready "$OPTIONAL_WORKSPACE" "$STATE_DIR/status-optional-running.json"
"$CLI" connect "$OPTIONAL_WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "echo OPTIONAL_MEDIATION_RUNNING" \
  --ready-timeout 30 \
  --timeout 10 >"$STATE_DIR/connect-optional.txt"
"$CLI" status "$OPTIONAL_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-optional-after-connect.json"
"$CLI" halt "$OPTIONAL_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt-optional.json"
"$CLI" delete "$OPTIONAL_WORKSPACE" --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete-optional.json"

python3 - "$STATE_DIR" <<'PY'
import json
import os
import sys

state_dir = sys.argv[1]

def read_json(name):
    with open(os.path.join(state_dir, name), "r", encoding="utf-8") as f:
        return json.load(f)

def read_text(name):
    with open(os.path.join(state_dir, name), "r", encoding="utf-8", errors="replace") as f:
        return f.read()

doctor = read_json("doctor.json")
create = read_json("create.json")
running = read_json("status-running.json")
after = read_json("status-after-connect.json")
quarantine = read_json("quarantine.json")
halt = read_json("halt.json")
delete = read_json("delete.json")
runtime = read_json("runtime-after-connect.json")
optional_create = read_json("create-optional.json")
optional_running = read_json("status-optional-running.json")
optional_after = read_json("status-optional-after-connect.json")
optional_halt = read_json("halt-optional.json")
optional_delete = read_json("delete-optional.json")

if doctor.get("host", {}).get("vsockAvailable") is not True:
    raise SystemExit(doctor)
mediation = create.get("mediation") or create.get("response", {}).get("mediation") or {}
if create.get("workspace") != "mediation-smoke":
    raise SystemExit(create)
if create.get("response", {}).get("event", {}).get("state") != "prepared":
    raise SystemExit(create)
if running.get("event", {}).get("state") != "running":
    raise SystemExit(running)
if running.get("mediation", {}).get("required") is not True:
    raise SystemExit(running)
if running.get("readiness", {}).get("mediationReady", {}).get("ready") is not True:
    raise SystemExit(running)
if "MEDIATION_OK" not in read_text("connect.txt"):
    raise SystemExit(read_text("connect.txt"))
if "RAW_VSOCK_OK" not in read_text("connect.txt"):
    raise SystemExit(read_text("connect.txt"))
if "RAW_LARGE_BEGIN" not in read_text("connect.txt") or "RAW_LARGE_END" not in read_text("connect.txt"):
    raise SystemExit(read_text("connect.txt"))
connect_lines = {line.strip() for line in read_text("connect.txt").splitlines()}
unavailable_lines = {line.strip() for line in read_text("connect-unavailable.txt").splitlines()}
if "RAW_UNAVAILABLE_FAILED" not in unavailable_lines or "RAW_UNAVAILABLE_UNEXPECTED" in unavailable_lines:
    raise SystemExit(read_text("connect-unavailable.txt"))
request = read_text("host-request.txt")
if "GET /mediation-check" not in request:
    raise SystemExit(request)
raw_request = read_text("raw-host-request.txt")
if "GET /raw-vsock-check" not in raw_request:
    raise SystemExit(raw_request)
raw_large_request = read_text("raw-large-host-request.txt")
if "GET /raw-large-check" not in raw_large_request:
    raise SystemExit(raw_large_request)
listeners = runtime.get("config", {}).get("vsockListeners") or []
listener_ports = {item.get("port") for item in listeners if str(item.get("target", "")).startswith("127.0.0.1:")}
if not {2050, 2051, 2052}.issubset(listener_ports):
    raise SystemExit(runtime)
if after.get("readiness", {}).get("mediationReady", {}).get("ready") is not True:
    raise SystemExit(after)
if quarantine.get("event", {}).get("state") != "quarantined":
    raise SystemExit(quarantine)
if halt.get("event", {}).get("state") != "halted":
    raise SystemExit(halt)
if delete.get("event", {}).get("state") != "stopped":
    raise SystemExit(delete)
if optional_create.get("workspace") != "mediation-optional":
    raise SystemExit(optional_create)
if optional_create.get("response", {}).get("event", {}).get("state") != "prepared":
    raise SystemExit(optional_create)
if optional_running.get("event", {}).get("state") != "running":
    raise SystemExit(optional_running)
if optional_running.get("mediation", {}).get("required") is not False:
    raise SystemExit(optional_running)
if optional_running.get("mediation", {}).get("failClosed") is not False:
    raise SystemExit(optional_running)
optional_mediation = optional_running.get("readiness", {}).get("mediationReady", {})
if optional_mediation.get("ready") is not False or optional_mediation.get("error"):
    raise SystemExit(optional_running)
if "OPTIONAL_MEDIATION_RUNNING" not in read_text("connect-optional.txt"):
    raise SystemExit(read_text("connect-optional.txt"))
if optional_after.get("event", {}).get("state") != "running":
    raise SystemExit(optional_after)
if optional_halt.get("event", {}).get("state") != "halted":
    raise SystemExit(optional_halt)
if optional_delete.get("event", {}).get("state") != "stopped":
    raise SystemExit(optional_delete)
PY

echo "microagent E2E mediation passed"
