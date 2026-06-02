#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"
SUPERVISOR="${MICROAGENT_APPLEVF_SUPERVISOR:-$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor}"
KERNEL="${MICROAGENT_APPLEVF_KERNEL:-$HOME/.microagent/kernels/apple-vf/arm64/Image}"
if [ ! -r "$KERNEL" ] && [ -r "$HOME/.microagent/kernels/apple-vf/Image" ]; then
  KERNEL="$HOME/.microagent/kernels/apple-vf/Image"
fi
IMAGE="${MICROAGENT_APPLEVF_MEDIATION_IMAGE:-docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6}"
ARCH="${MICROAGENT_APPLEVF_MEDIATION_ARCH:-arm64}"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-applevf-mediation.XXXXXX")"
HOST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-applevf-mediation-host.XXXXXX")"
WORKSPACE="mediation-smoke"
FAIL_WORKSPACE="mediation-fail-smoke"
RAW_WORKSPACE="mediation-raw-smoke"
OPTIONAL_WORKSPACE="mediation-optional-smoke"
CLI="$STATE_DIR/microagent"
GUEST_INIT="$STATE_DIR/microagent-guestinit"
PROBE="$HOST_DIR/mediation-probe"
SPEC="$HOST_DIR/microagent.yaml"
FAIL_SPEC="$HOST_DIR/microagent-fail.yaml"
SERVER_LOG="$HOST_DIR/server.log"
OBSERVED="$HOST_DIR/observed.jsonl"
RESPONSE="$STATE_DIR/response.json"
FAIL_RESPONSE="$STATE_DIR/fail-response.json"
HOST_PORT=""
FAIL_HOST_PORT=""
SERVER_PID=""
RAW_SERVER_PID=""
RAW_LARGE_SERVER_PID=""

cleanup() {
  status="$?"
  set +e
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" >/dev/null 2>&1
    wait "$SERVER_PID" >/dev/null 2>&1
  fi
  if [ -n "$RAW_SERVER_PID" ]; then
    kill "$RAW_SERVER_PID" >/dev/null 2>&1
    wait "$RAW_SERVER_PID" >/dev/null 2>&1
  fi
  if [ -n "$RAW_LARGE_SERVER_PID" ]; then
    kill "$RAW_LARGE_SERVER_PID" >/dev/null 2>&1
    wait "$RAW_LARGE_SERVER_PID" >/dev/null 2>&1
  fi
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_APPLEVF_MEDIATION_SMOKE:-0}" != "1" ]; then
    if [ -x "$CLI" ]; then
      "$CLI" stop "$WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
      "$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
      "$CLI" stop "$FAIL_WORKSPACE" --state-dir "$STATE_DIR/fail" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
      "$CLI" delete "$FAIL_WORKSPACE" --yes --state-dir "$STATE_DIR/fail" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
      "$CLI" stop "$RAW_WORKSPACE" --state-dir "$STATE_DIR/raw" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
      "$CLI" delete "$RAW_WORKSPACE" --yes --state-dir "$STATE_DIR/raw" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
      "$CLI" stop "$OPTIONAL_WORKSPACE" --state-dir "$STATE_DIR/optional" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
      "$CLI" delete "$OPTIONAL_WORKSPACE" --yes --state-dir "$STATE_DIR/optional" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    fi
    rm -rf "$STATE_DIR" "$HOST_DIR"
  else
    if [ -x "$CLI" ]; then
      "$CLI" stop "$WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
      "$CLI" stop "$FAIL_WORKSPACE" --state-dir "$STATE_DIR/fail" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
      "$CLI" stop "$RAW_WORKSPACE" --state-dir "$STATE_DIR/raw" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
      "$CLI" stop "$OPTIONAL_WORKSPACE" --state-dir "$STATE_DIR/optional" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    fi
    echo "kept Apple VF mediation smoke state at $STATE_DIR" >&2
    echo "kept Apple VF mediation smoke host dir at $HOST_DIR" >&2
    [ -f "$SERVER_LOG" ] && tail -n 120 "$SERVER_LOG" >&2
    [ -f "$STATE_DIR/$WORKSPACE/serial.log" ] && tail -n 240 "$STATE_DIR/$WORKSPACE/serial.log" >&2
  fi
}
trap cleanup EXIT INT TERM HUP

if [ "$(uname -s)" != "Darwin" ] || [ "$(uname -m)" != "arm64" ]; then
  e2e_skip "Apple VF mediation smoke requires macOS on Apple silicon"
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

pick_port() {
  python3 - <<'PY'
import socket

with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
    s.bind(("127.0.0.1", 0))
    print(s.getsockname()[1])
PY
}

wait_for_status_ready() {
  workspace="$1"
  state_dir="$2"
  output="$3"
  deadline=$((SECONDS + ${MICROAGENT_APPLEVF_MEDIATION_TIMEOUT_SECONDS:-60}))
  while true; do
    "$CLI" status "$workspace" --state-dir "$state_dir" >"$output"
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

cat >"$HOST_DIR/mediation-probe.go" <<'GO'
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	port := uint32(2048)
	if raw := strings.TrimSpace(os.Getenv("MEDIATION_PORT")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 32)
		if err != nil || parsed == 0 {
			fmt.Fprintf(os.Stderr, "invalid MEDIATION_PORT %q\n", raw)
			os.Exit(2)
		}
		port = uint32(parsed)
	}
	fd, err := dialHostVsock(port, 15*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial mediation vsock port %d: %v\n", port, err)
		os.Exit(1)
	}
	defer unix.Close(fd)
	file := os.NewFile(uintptr(fd), "mediation-vsock")
	if file == nil {
		fmt.Fprintln(os.Stderr, "wrap mediation fd")
		os.Exit(1)
	}
	defer file.Close()
	if _, err := file.WriteString("{\"signal\":\"ready\",\"runtimeID\":\"mediation-smoke\"}\n"); err != nil {
		fmt.Fprintf(os.Stderr, "write mediation ready: %v\n", err)
		os.Exit(1)
	}
	line, err := bufio.NewReader(file).ReadString('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "read mediation response: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("MEDIATION_REPLY=%s", line)
	if !strings.Contains(line, `"ok":true`) {
		fmt.Fprintf(os.Stderr, "unexpected mediation response: %s", line)
		os.Exit(1)
	}
	fmt.Println("MEDIATION_OK")
}

func dialHostVsock(port uint32, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
		if err == nil {
			err = unix.Connect(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: port})
			if err == nil {
				return fd, nil
			}
			_ = unix.Close(fd)
		}
		lastErr = err
		if time.Now().After(deadline) {
			return -1, lastErr
		}
		time.Sleep(50 * time.Millisecond)
	}
}
GO

(
  cd "$ROOT"
  go build -o "$CLI" ./cmd/microagent
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -o "$GUEST_INIT" ./cmd/microagent-guestinit
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -o "$PROBE" "$HOST_DIR/mediation-probe.go"
)

HOST_PORT="$(pick_port)"
FAIL_HOST_PORT="$(pick_port)"
RAW_HOST_PORT="$(pick_port)"
RAW_LARGE_HOST_PORT="$(pick_port)"
RAW_UNAVAILABLE_PORT="$(pick_port)"
OPTIONAL_HOST_PORT="$(pick_port)"

cat >"$SPEC" <<YAML
name: $WORKSPACE
image: $IMAGE
entrypoint: /usr/local/bin/mediation-probe
setup:
  - /usr/local/bin/mediation-probe
env:
  MEDIATION_PORT: "2048"
mediation:
  enabled: true
  required: true
  port: 2048
  target: 127.0.0.1:$HOST_PORT
  failClosed: true
files:
  - src: $PROBE
    dst: /usr/local/bin/mediation-probe
    mode: "0755"
YAML

cat >"$FAIL_SPEC" <<YAML
name: $FAIL_WORKSPACE
image: $IMAGE
entrypoint: /usr/local/bin/mediation-probe
env:
  MEDIATION_PORT: "2048"
mediation:
  enabled: true
  required: true
  port: 2048
  target: 127.0.0.1:$FAIL_HOST_PORT
  failClosed: true
files:
  - src: $PROBE
    dst: /usr/local/bin/mediation-probe
    mode: "0755"
YAML

"$CLI" create \
  --backend apple-vf \
  --file "$FAIL_SPEC" \
  --arch "$ARCH" \
  --kernel "$KERNEL" \
  --state-dir "$STATE_DIR/fail" \
  --size-mib "${MICROAGENT_APPLEVF_MEDIATION_SIZE_MIB:-128}" \
  --mke2fs "$MKE2FS" \
  --guest-init "$GUEST_INIT" \
  --supervisor "$SUPERVISOR" \
  --memory "${MICROAGENT_APPLEVF_MEDIATION_MEMORY_MIB:-512}" \
  --cpus "${MICROAGENT_APPLEVF_MEDIATION_CPUS:-2}" >"$STATE_DIR/fail-create.json"

"$CLI" start "$FAIL_WORKSPACE" \
  --state-dir "$STATE_DIR/fail" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" >"$STATE_DIR/fail-start.json"

wait_for_result() {
  output="$1"
  state_dir="$2"
  workspace="$3"
  deadline=$((SECONDS + ${MICROAGENT_APPLEVF_MEDIATION_TIMEOUT_SECONDS:-60}))
  while true; do
    if "$CLI" result "$workspace" --state-dir "$state_dir" >"$output" 2>"$output.err"; then
      break
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      cat "$output.err" >&2 || true
      echo "mediation workspace $workspace did not produce a result before timeout" >&2
      exit 1
    fi
    sleep 1
  done
}

wait_for_result "$FAIL_RESPONSE" "$STATE_DIR/fail" "$FAIL_WORKSPACE"

python3 - "$FAIL_RESPONSE" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)
guest = result.get("result") or {}
exit_code = guest.get("exit_code", guest.get("exitCode"))
if exit_code == 0:
    raise SystemExit(result)
stderr = guest.get("stderr", "")
serial = result.get("serial_log", "")
if "dial mediation vsock port 2048" not in stderr and "dial mediation vsock port 2048" not in serial:
    raise SystemExit(result)
response = result.get("response") or {}
readiness = response.get("readiness") or {}
mediation = readiness.get("mediationReady") or {}
if mediation.get("ready") is True:
    raise SystemExit(readiness)
PY

"$CLI" create \
  --backend apple-vf \
  --file "$SPEC" \
  --arch "$ARCH" \
  --kernel "$KERNEL" \
  --state-dir "$STATE_DIR" \
  --size-mib "${MICROAGENT_APPLEVF_MEDIATION_SIZE_MIB:-128}" \
  --mke2fs "$MKE2FS" \
  --guest-init "$GUEST_INIT" \
  --supervisor "$SUPERVISOR" \
  --memory "${MICROAGENT_APPLEVF_MEDIATION_MEMORY_MIB:-512}" \
  --cpus "${MICROAGENT_APPLEVF_MEDIATION_CPUS:-2}" >"$STATE_DIR/create.json"

python3 - "$HOST_PORT" "$OBSERVED" >"$SERVER_LOG" 2>&1 <<'PY' &
import json
import socket
import sys

port = int(sys.argv[1])
observed = sys.argv[2]
with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as srv:
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("127.0.0.1", port))
    srv.listen(1)
    print(f"listening on 127.0.0.1:{port}", flush=True)
    conn, addr = srv.accept()
    with conn:
        conn.settimeout(15)
        data = b""
        while b"\n" not in data:
            chunk = conn.recv(4096)
            if not chunk:
                raise SystemExit("guest closed before newline")
            data += chunk
        line = data.split(b"\n", 1)[0].decode("utf-8")
        msg = json.loads(line)
        with open(observed, "a", encoding="utf-8") as f:
            f.write(json.dumps({"addr": addr, "message": msg}, sort_keys=True) + "\n")
        conn.sendall(b'{"ok":true,"request_id":"req-mediation-smoke"}\n')
PY
SERVER_PID="$!"

for _ in $(seq 1 80); do
  if grep -q "listening on" "$SERVER_LOG" 2>/dev/null; then
    break
  fi
  if ! kill -0 "$SERVER_PID" >/dev/null 2>&1; then
    cat "$SERVER_LOG" >&2 || true
    exit 1
  fi
  sleep 0.25
done
if ! grep -q "listening on" "$SERVER_LOG" 2>/dev/null; then
  cat "$SERVER_LOG" >&2 || true
  echo "mediation host listener did not become ready" >&2
  exit 1
fi

"$CLI" start "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" >"$STATE_DIR/start.json"
wait_for_result "$RESPONSE" "$STATE_DIR" "$WORKSPACE"

python3 - "$RESPONSE" "$OBSERVED" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)
with open(sys.argv[2], "r", encoding="utf-8") as f:
    observed = [json.loads(line) for line in f if line.strip()]
guest = result.get("result") or {}
stdout = guest.get("stdout", "")
exit_code = guest.get("exit_code", guest.get("exitCode"))
if exit_code != 0:
    raise SystemExit(result)
if "MEDIATION_OK" not in stdout:
    raise SystemExit(result)
if not observed:
    raise SystemExit("host listener did not observe a mediation message")
message = observed[0].get("message") or {}
if message.get("signal") != "ready" or message.get("runtimeID") != "mediation-smoke":
    raise SystemExit(observed)
readiness = result.get("response", {}).get("readiness", {})
mediation = readiness.get("mediationReady", {})
if mediation and result.get("response", {}).get("event", {}).get("state") == "running" and mediation.get("ready") is not True:
    raise SystemExit(readiness)
PY

python3 - "$RAW_HOST_PORT" "$HOST_DIR/raw-host-request.txt" <<'PY' &
import socket
import sys

port = int(sys.argv[1])
request_file = sys.argv[2]
with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as srv:
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("127.0.0.1", port))
    srv.listen(1)
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
PY
RAW_SERVER_PID="$!"

python3 - "$RAW_LARGE_HOST_PORT" "$HOST_DIR/raw-large-host-request.txt" <<'PY' &
import socket
import sys

port = int(sys.argv[1])
request_file = sys.argv[2]
with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as srv:
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("127.0.0.1", port))
    srv.listen(1)
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
PY
RAW_LARGE_SERVER_PID="$!"

"$CLI" create "$RAW_WORKSPACE" \
  --backend apple-vf \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --kernel "$KERNEL" \
  --state-dir "$STATE_DIR/raw" \
  --size-mib "${MICROAGENT_APPLEVF_MEDIATION_SIZE_MIB:-128}" \
  --mke2fs "$MKE2FS" \
  --guest-init "$GUEST_INIT" \
  --supervisor "$SUPERVISOR" \
  --memory "${MICROAGENT_APPLEVF_MEDIATION_MEMORY_MIB:-512}" \
  --cpus "${MICROAGENT_APPLEVF_MEDIATION_CPUS:-2}" \
  --network isolated \
  --env MICROAGENT_VSOCK_TCP_LISTENERS=127.0.0.1:18081=2050,127.0.0.1:18082=2051,127.0.0.1:18083=2052 \
  --service-command "sleep 300" >"$STATE_DIR/raw-create.json"

"$CLI" start "$RAW_WORKSPACE" \
  --state-dir "$STATE_DIR/raw" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" \
  --vsock "2050=127.0.0.1:${RAW_HOST_PORT}" \
  --vsock "2051=127.0.0.1:${RAW_LARGE_HOST_PORT}" \
  --vsock "2052=127.0.0.1:${RAW_UNAVAILABLE_PORT}" >"$STATE_DIR/raw-start.json"
wait_for_status_ready "$RAW_WORKSPACE" "$STATE_DIR/raw" "$STATE_DIR/raw-status-running.json"
"$CLI" connect "$RAW_WORKSPACE" \
  --state-dir "$STATE_DIR/raw" \
  --send "wget -qO- -T 10 http://127.0.0.1:18081/raw-vsock-check; wget -qO- -T 10 http://127.0.0.1:18082/raw-large-check > /tmp/raw-large.out; cat /tmp/raw-large.out" \
  --ready-timeout 30 \
  --timeout 15 >"$STATE_DIR/raw-connect.txt"
"$CLI" connect "$RAW_WORKSPACE" \
  --state-dir "$STATE_DIR/raw" \
  --send "if wget -qO- -T 3 http://127.0.0.1:18083/raw-unavailable; then echo RAW_UNAVAILABLE_UNEXPECTED; else echo RAW_UNAVAILABLE_FAILED; fi" \
  --ready-timeout 30 \
  --timeout 10 >"$STATE_DIR/raw-connect-unavailable.txt"
wait "$RAW_SERVER_PID"
RAW_SERVER_PID=""
wait "$RAW_LARGE_SERVER_PID"
RAW_LARGE_SERVER_PID=""
"$CLI" status "$RAW_WORKSPACE" --state-dir "$STATE_DIR/raw" >"$STATE_DIR/raw-status-after-connect.json"
cp "$STATE_DIR/raw/$RAW_WORKSPACE/runtime.json" "$STATE_DIR/raw-runtime-after-connect.json"
"$CLI" halt "$RAW_WORKSPACE" --state-dir "$STATE_DIR/raw" --supervisor "$SUPERVISOR" >"$STATE_DIR/raw-halt.json"
"$CLI" delete "$RAW_WORKSPACE" --yes --state-dir "$STATE_DIR/raw" --supervisor "$SUPERVISOR" >"$STATE_DIR/raw-delete.json"

"$CLI" create "$OPTIONAL_WORKSPACE" \
  --backend apple-vf \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --kernel "$KERNEL" \
  --state-dir "$STATE_DIR/optional" \
  --size-mib "${MICROAGENT_APPLEVF_MEDIATION_SIZE_MIB:-128}" \
  --mke2fs "$MKE2FS" \
  --guest-init "$GUEST_INIT" \
  --supervisor "$SUPERVISOR" \
  --memory "${MICROAGENT_APPLEVF_MEDIATION_MEMORY_MIB:-512}" \
  --cpus "${MICROAGENT_APPLEVF_MEDIATION_CPUS:-2}" \
  --network isolated \
  --mediation "2049=127.0.0.1:${OPTIONAL_HOST_PORT}" \
  --mediation-optional \
  --service-command "sleep 300" >"$STATE_DIR/optional-create.json"
"$CLI" start "$OPTIONAL_WORKSPACE" \
  --state-dir "$STATE_DIR/optional" \
  --kernel "$KERNEL" \
  --supervisor "$SUPERVISOR" >"$STATE_DIR/optional-start.json"
wait_for_status_ready "$OPTIONAL_WORKSPACE" "$STATE_DIR/optional" "$STATE_DIR/optional-status-running.json"
"$CLI" connect "$OPTIONAL_WORKSPACE" \
  --state-dir "$STATE_DIR/optional" \
  --send "printf 'OPTIONAL_MEDIATION_RUNNING\n'; sleep 1" \
  --ready-timeout 30 \
  --timeout 10 >"$STATE_DIR/optional-connect.txt"
"$CLI" status "$OPTIONAL_WORKSPACE" --state-dir "$STATE_DIR/optional" >"$STATE_DIR/optional-status-after-connect.json"
"$CLI" halt "$OPTIONAL_WORKSPACE" --state-dir "$STATE_DIR/optional" --supervisor "$SUPERVISOR" >"$STATE_DIR/optional-halt.json"
"$CLI" delete "$OPTIONAL_WORKSPACE" --yes --state-dir "$STATE_DIR/optional" --supervisor "$SUPERVISOR" >"$STATE_DIR/optional-delete.json"

python3 - "$STATE_DIR" "$HOST_DIR" <<'PY'
import json
import os
import sys

state_dir, host_dir = sys.argv[1:3]

def read_json(name):
    with open(os.path.join(state_dir, name), "r", encoding="utf-8") as f:
        return json.load(f)

def read_text(path):
    with open(path, "r", encoding="utf-8", errors="replace") as f:
        return f.read()

raw_create = read_json("raw-create.json")
raw_running = read_json("raw-status-running.json")
raw_after = read_json("raw-status-after-connect.json")
raw_runtime = read_json("raw-runtime-after-connect.json")
raw_halt = read_json("raw-halt.json")
raw_delete = read_json("raw-delete.json")
optional_create = read_json("optional-create.json")
optional_running = read_json("optional-status-running.json")
optional_after = read_json("optional-status-after-connect.json")
optional_halt = read_json("optional-halt.json")
optional_delete = read_json("optional-delete.json")
raw_connect = read_text(os.path.join(state_dir, "raw-connect.txt"))
raw_unavailable = read_text(os.path.join(state_dir, "raw-connect-unavailable.txt"))
raw_request = read_text(os.path.join(host_dir, "raw-host-request.txt"))
raw_large_request = read_text(os.path.join(host_dir, "raw-large-host-request.txt"))
optional_connect = read_text(os.path.join(state_dir, "optional-connect.txt"))

if raw_create.get("response", {}).get("event", {}).get("state") != "prepared":
    raise SystemExit(raw_create)
if raw_running.get("event", {}).get("state") != "running":
    raise SystemExit(raw_running)
if "RAW_VSOCK_OK" not in raw_connect:
    raise SystemExit(raw_connect)
if "RAW_LARGE_BEGIN" not in raw_connect or "RAW_LARGE_END" not in raw_connect:
    raise SystemExit(raw_connect)
if "RAW_UNAVAILABLE_FAILED" not in raw_unavailable or "RAW_UNAVAILABLE_UNEXPECTED" in raw_unavailable:
    raise SystemExit(raw_unavailable)
if "GET /raw-vsock-check" not in raw_request:
    raise SystemExit(raw_request)
if "GET /raw-large-check" not in raw_large_request:
    raise SystemExit(raw_large_request)
listeners = raw_runtime.get("config", {}).get("vsockListeners") or []
ports = {item.get("port") for item in listeners}
if not {2050, 2051, 2052}.issubset(ports):
    raise SystemExit(raw_runtime)
if raw_halt.get("event", {}).get("state") != "halted":
    raise SystemExit(raw_halt)
if raw_delete.get("event", {}).get("state") != "stopped":
    raise SystemExit(raw_delete)
if optional_create.get("response", {}).get("event", {}).get("state") != "prepared":
    raise SystemExit(optional_create)
if optional_running.get("event", {}).get("state") != "running":
    raise SystemExit(optional_running)
mediation = optional_running.get("mediation") or {}
if mediation.get("required") is not False or mediation.get("failClosed") is not False:
    raise SystemExit(optional_running)
ready = optional_running.get("readiness", {}).get("mediationReady", {})
if ready.get("ready") is not True or ready.get("error"):
    raise SystemExit(optional_running)
if "OPTIONAL_MEDIATION_RUNNING" not in optional_connect:
    raise SystemExit(optional_connect)
if optional_after.get("event", {}).get("state") != "running":
    raise SystemExit(optional_after)
if optional_halt.get("event", {}).get("state") != "halted":
    raise SystemExit(optional_halt)
if optional_delete.get("event", {}).get("state") != "stopped":
    raise SystemExit(optional_delete)
PY

echo "Apple VF mediation smoke passed"
