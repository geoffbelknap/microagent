#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export PATH="/home/linuxbrew/.linuxbrew/bin:/home/linuxbrew/.linuxbrew/sbin:/opt/homebrew/bin:/opt/homebrew/sbin:$PATH"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-networking.XXXXXX")"
CLI="$STATE_DIR/microagent"
DEFAULT_SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
CACHED_SUPERVISOR="$ROOT/.cache/microagent-e2e/bin/microagent-firecracker-supervisor"
if [ -n "${MICROAGENT_FIRECRACKER_SUPERVISOR:-}" ]; then
  SUPERVISOR="$MICROAGENT_FIRECRACKER_SUPERVISOR"
elif [ -x "$CACHED_SUPERVISOR" ]; then
  SUPERVISOR="$CACHED_SUPERVISOR"
else
  SUPERVISOR="$DEFAULT_SUPERVISOR"
fi
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
WORKSPACE="nats-e2e"
NAT_WORKSPACE="nat-outbound"
BRIDGED_WORKSPACE="bridged-ready"
STATIC_WORKSPACE="static-net"
APPLY_WORKSPACE="apply-stopped"
ARTIFACT_DIR="$STATE_DIR/artifacts"
IMAGE="${MICROAGENT_NATS_IMAGE:-docker.io/library/nats@sha256:6e0cca2c6da79f0a3542ec5a3319dd10b1b05f5d8e8949afa8e9cdf6314bbf6c}"
IMAGE_CACHE_STATE="${MICROAGENT_E2E_IMAGE_CACHE_DIR:-$ROOT/.cache/microagent-e2e/image-cache/nats-amd64}"
EXPECTED_KERNEL_SHA="4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0"
BRIDGE_NAME="${MICROAGENT_E2E_BRIDGE:-}"
DELETE_BRIDGE=0
ORIGINAL_IP_FORWARD=""

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    "$CLI" stop "$NAT_WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" stop "$BRIDGED_WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" stop "$STATIC_WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" stop "$APPLY_WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    if [ "$status" -eq 0 ]; then
      "$CLI" stop "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      "$CLI" delete "$NAT_WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      "$CLI" delete "$BRIDGED_WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      "$CLI" delete "$STATIC_WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      "$CLI" delete "$APPLY_WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      "$CLI" delete "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    else
      "$CLI" stop "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    fi
  fi
  if command -v ip >/dev/null 2>&1; then
    if [ "$DELETE_BRIDGE" = "1" ] && [ -n "$BRIDGE_NAME" ]; then
      ip link delete "$BRIDGE_NAME" type bridge >/dev/null 2>&1 || true
    fi
  fi
  if [ -n "$ORIGINAL_IP_FORWARD" ] && [ -e /proc/sys/net/ipv4/ip_forward ]; then
    sysctl -w "net.ipv4.ip_forward=$ORIGINAL_IP_FORWARD" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_NETWORKING:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E networking state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    echo "microagent E2E networking requires Linux amd64" >&2
    exit 2
    ;;
esac

for required in pasta getcap ip debugfs; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "$required is required for microagent E2E networking" >&2
    exit 2
  fi
done

if [ ! -e /dev/kvm ]; then
  echo "/dev/kvm is not visible; run this smoke outside sandboxed environments" >&2
  exit 2
fi

if [ ! -e /dev/net/tun ]; then
  echo "/dev/net/tun is not visible; user networking requires tun" >&2
  exit 2
fi

if [ -e /proc/sys/kernel/unprivileged_userns_clone ] && [ "$(cat /proc/sys/kernel/unprivileged_userns_clone)" != "1" ]; then
  echo "kernel.unprivileged_userns_clone is disabled" >&2
  exit 2
fi
if [ -e /proc/sys/user/max_user_namespaces ] && [ "$(cat /proc/sys/user/max_user_namespaces)" = "0" ]; then
  echo "user.max_user_namespaces is 0" >&2
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

pick_port() {
  python3 - <<'PY'
import socket

with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
    s.bind(("127.0.0.1", 0))
    print(s.getsockname()[1])
PY
}

nats_port="$(pick_port)"
monitor_port="$(pick_port)"
apply_port="$(pick_port)"

export GOCACHE="$STATE_DIR/gocache"
export GOMODCACHE="$STATE_DIR/gomodcache"
export GOFLAGS="${GOFLAGS:-} -modcacherw"
export MICROAGENT_FIRECRACKER="$firecracker"
export MICROAGENT_FIRECRACKER_SUPERVISOR="$SUPERVISOR"

supervisor_has_network_caps() {
  caps="$(getcap "$SUPERVISOR" 2>/dev/null || true)"
  [[ "$caps" == *cap_net_admin* && "$caps" == *cap_setpcap* ]]
}

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

nats_assert() {
  mode="$1"
  port="$2"
  out="$3"
  python3 - "$mode" "$port" "$out" <<'PY'
import json
import socket
import sys
import time

mode = sys.argv[1]
port = int(sys.argv[2])
out = sys.argv[3]

def recv_line(sock):
    data = bytearray()
    while not data.endswith(b"\r\n"):
        chunk = sock.recv(1)
        if not chunk:
            raise RuntimeError("connection closed")
        data.extend(chunk)
    return bytes(data)

def recv_payload(sock, size):
    data = bytearray()
    while len(data) < size + 2:
        chunk = sock.recv(size + 2 - len(data))
        if not chunk:
            raise RuntimeError("connection closed")
        data.extend(chunk)
    if not data.endswith(b"\r\n"):
        raise RuntimeError(f"payload missing CRLF: {data!r}")
    return bytes(data[:-2])

def connect(deadline):
    last_error = ""
    while time.time() < deadline:
        try:
            sock = socket.create_connection(("127.0.0.1", port), timeout=2)
            sock.settimeout(3)
            info = recv_line(sock)
            if not info.startswith(b"INFO "):
                raise RuntimeError(info.decode("utf-8", errors="replace"))
            sock.sendall(b'CONNECT {"verbose":false,"pedantic":false}\r\nPING\r\n')
            if recv_line(sock) != b"PONG\r\n":
                raise RuntimeError("NATS did not respond to PING")
            return sock
        except Exception as err:
            last_error = str(err)
            try:
                sock.close()
            except Exception:
                pass
            time.sleep(0.2)
    raise RuntimeError(f"NATS did not become ready: {last_error}")

def monitor_varz(deadline):
    last_error = ""
    while time.time() < deadline:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=2) as sock:
                sock.settimeout(2)
                sock.sendall(b"GET /varz HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n")
                data = bytearray()
                while b"\r\n\r\n" not in data:
                    chunk = sock.recv(4096)
                    if not chunk:
                        raise RuntimeError("monitor closed before headers")
                    data.extend(chunk)
                raw = bytes(data)
                headers, _, body = raw.partition(b"\r\n\r\n")
                content_length = None
                for line in headers.decode("iso-8859-1", errors="replace").split("\r\n"):
                    name, _, value = line.partition(":")
                    if name.lower() == "content-length":
                        content_length = int(value.strip())
                        break
                if content_length is not None:
                    while len(body) < content_length:
                        chunk = sock.recv(content_length - len(body))
                        if not chunk:
                            break
                        body += chunk
                    body = body[:content_length]
                else:
                    while True:
                        chunk = sock.recv(4096)
                        if not chunk:
                            break
                        body += chunk
                parsed = json.loads(body.decode())
                if parsed.get("server_id") and parsed.get("jetstream", {}).get("config"):
                    return parsed
                last_error = body.decode(errors="replace")
        except Exception as err:
            last_error = str(err)
        time.sleep(0.2)
    raise RuntimeError(f"NATS monitor did not become ready: {last_error}")

deadline = time.time() + 25
if mode == "monitor":
    result = monitor_varz(deadline)
elif mode == "roundtrip":
    with connect(deadline) as sock:
        subject = f"e2e.roundtrip.{time.time_ns()}"
        payload = f"microagent-nats-roundtrip-{time.time_ns()}"
        sock.sendall(f"SUB {subject} 1\r\nPING\r\n".encode())
        while recv_line(sock) != b"PONG\r\n":
            pass
        encoded = payload.encode()
        sock.sendall(f"PUB {subject} {len(encoded)}\r\n".encode() + encoded + b"\r\nPING\r\n")
        received = None
        while time.time() < deadline:
            line = recv_line(sock)
            if line.startswith(b"PING"):
                sock.sendall(b"PONG\r\n")
                continue
            if line == b"PONG\r\n":
                continue
            if line.startswith(b"MSG "):
                parts = line.decode().strip().split()
                received = recv_payload(sock, int(parts[-1])).decode()
                break
        if received != payload:
            raise RuntimeError({"sent": payload, "received": received})
        result = {"subject": subject, "payload": received}
else:
    raise RuntimeError(f"unknown mode {mode}")

with open(out, "w", encoding="utf-8") as f:
    json.dump(result, f, indent=2, sort_keys=True)
    f.write("\n")
PY
}

image_cache_has_ref() {
  cache_state="$1"
  python3 - "$cache_state/images/index.json" "$IMAGE" <<'PY'
import json
import os
import sys

index_path, image_ref = sys.argv[1:3]
try:
    with open(index_path, "r", encoding="utf-8") as f:
        index = json.load(f)
except FileNotFoundError:
    raise SystemExit(1)
for image in index.get("images", []):
    refs = {image.get("image_ref"), image.get("resolved_ref"), image.get("digest")}
    if image_ref not in refs:
        continue
    platform = image.get("platform") or {}
    if platform.get("os") not in ("", "linux") or platform.get("architecture") != "amd64":
        continue
    output_path = image.get("output_path") or ""
    if output_path and os.path.exists(output_path):
        raise SystemExit(0)
raise SystemExit(1)
PY
}

ensure_cached_image() {
  mkdir -p "$IMAGE_CACHE_STATE"
  if [ "${MICROAGENT_E2E_REFRESH_IMAGE_CACHE:-0}" != "1" ] && image_cache_has_ref "$IMAGE_CACHE_STATE"; then
    echo "using cached networking image rootfs for $IMAGE" >&2
    return 0
  fi
  echo "refreshing cached networking image rootfs for $IMAGE" >&2
  "$CLI" images pull "$IMAGE" \
    --state-dir "$IMAGE_CACHE_STATE" \
    --arch amd64 \
    --guest-init "$GUEST_INIT" \
    --size-mib 192 >"$STATE_DIR/image-cache-pull.json"
}

seed_image_cache_for_state() {
  target_state="$1"
  mkdir -p "$target_state/images"
  python3 - "$IMAGE_CACHE_STATE/images/index.json" "$target_state/images/index.json" "$IMAGE" <<'PY'
import json
import os
import sys

source, target, image_ref = sys.argv[1:4]
with open(source, "r", encoding="utf-8") as f:
    index = json.load(f)
for image in index.get("images", []):
    refs = {image.get("image_ref"), image.get("resolved_ref"), image.get("digest")}
    if image_ref not in refs:
        continue
    platform = image.get("platform") or {}
    if platform.get("os") not in ("", "linux") or platform.get("architecture") != "amd64":
        continue
    output_path = image.get("output_path") or ""
    if output_path and os.path.exists(output_path):
        with open(target, "w", encoding="utf-8") as out:
            json.dump({"images": [image]}, out, indent=2, sort_keys=True)
            out.write("\n")
        raise SystemExit(0)
raise SystemExit(f"cached image {image_ref} is missing from {source}")
PY
}

cached_image_rootfs_path() {
  python3 - "$IMAGE_CACHE_STATE/images/index.json" "$IMAGE" <<'PY'
import json
import os
import sys

source, image_ref = sys.argv[1:3]
with open(source, "r", encoding="utf-8") as f:
    index = json.load(f)
for image in index.get("images", []):
    refs = {image.get("image_ref"), image.get("resolved_ref"), image.get("digest")}
    if image_ref not in refs:
        continue
    platform = image.get("platform") or {}
    if platform.get("os") not in ("", "linux") or platform.get("architecture") != "amd64":
        continue
    output_path = image.get("output_path") or ""
    if output_path and os.path.exists(output_path):
        print(output_path)
        raise SystemExit(0)
raise SystemExit(f"cached image {image_ref} is missing")
PY
}

prepare_cached_workspace() {
  name="$1"
  network_json="$2"
  artifacts_json="$3"
  output_json="$4"
  rootfs_source="$(cached_image_rootfs_path)"
  workspace_dir="$STATE_DIR/workspaces/$name"
  state_dir="$STATE_DIR/$name"
  mkdir -p "$workspace_dir" "$state_dir"
  cp "$rootfs_source" "$workspace_dir/rootfs.ext4"
  python3 - "$STATE_DIR" "$name" "$network_json" "$artifacts_json" "$output_json" <<'PY'
import json
import os
import sys
import time

state_dir, name, network_raw, artifacts_raw, output_json = sys.argv[1:6]
network = json.loads(network_raw)
artifacts = json.loads(artifacts_raw)
now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
manifest = {
    "name": name,
    "profile": "small",
    "restart": "never",
    "resources": {
        "memory_mib": 512,
        "cpu_count": 2,
        "size_mib": 192,
    },
    "network": network,
    "artifacts": artifacts,
}
event = {
    "identity": {
        "requestID": f"{name}-prepared",
        "runtimeID": name,
        "role": "workload",
        "backend": "firecracker",
    },
    "state": "prepared",
    "detail": "prepared from cached E2E image rootfs",
    "observedAt": now,
}
result = {
    "workspace": name,
    "state_dir": state_dir,
    "profile": "small",
    "restart": "never",
    "resources": manifest["resources"],
    "network": network,
    "rootfs_path": os.path.join(state_dir, "workspaces", name, "rootfs.ext4"),
    "artifacts": artifacts,
    "response": {
        "ok": True,
        "backend": "firecracker",
        "event": event,
    },
}
with open(os.path.join(state_dir, "workspaces", name, "workspace.json"), "w", encoding="utf-8") as f:
    json.dump(manifest, f, indent=2, sort_keys=True)
    f.write("\n")
with open(os.path.join(state_dir, name, "event.json"), "w", encoding="utf-8") as f:
    json.dump(event, f, indent=2, sort_keys=True)
    f.write("\n")
with open(output_json, "w", encoding="utf-8") as f:
    json.dump(result, f, indent=2, sort_keys=True)
    f.write("\n")
PY
}

write_guest_run_config() {
  target="$1"
  host_port_one="$2"
  guest_port_one="$3"
  host_port_two="$4"
  guest_port_two="$5"
  python3 - "$target" "$host_port_one" "$guest_port_one" "$host_port_two" "$guest_port_two" <<'PY'
import json
import sys

target, host_one, guest_one, host_two, guest_two = sys.argv[1:6]
config = {
    "command": [],
    "port": 1024,
    "hostForwards": [
        {"protocol": "tcp", "hostPort": int(host_one), "guestPort": int(guest_one)},
        {"protocol": "tcp", "hostPort": int(host_two), "guestPort": int(guest_two)},
    ],
}
with open(target, "w", encoding="utf-8") as f:
    json.dump(config, f, indent=2, sort_keys=True)
    f.write("\n")
PY
}

patch_manifest_port_forwards() {
  manifest="$1"
  nats_port="$2"
  monitor_port="$3"
  python3 - "$manifest" "$nats_port" "$monitor_port" <<'PY'
import json
import sys

path, nats_port, monitor_port = sys.argv[1:4]
with open(path, "r", encoding="utf-8") as f:
    manifest = json.load(f)
network = manifest.setdefault("network", {})
network["mode"] = "user"
network["port_forwards"] = [
    {"protocol": "tcp", "host": "127.0.0.1", "hostPort": int(nats_port), "guestPort": 4222},
    {"protocol": "tcp", "host": "127.0.0.1", "hostPort": int(monitor_port), "guestPort": 8222},
]
with open(path, "w", encoding="utf-8") as f:
    json.dump(manifest, f, indent=2, sort_keys=True)
    f.write("\n")
PY
}

(
  cd "$ROOT"
  go build -buildvcs=false -o "$CLI" ./cmd/microagent
  if [ "$SUPERVISOR" = "$DEFAULT_SUPERVISOR" ]; then
    go build -buildvcs=false -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
  elif [ "$SUPERVISOR" = "$CACHED_SUPERVISOR" ]; then
    if [ "$ROOT/pkg/supervisors/firecracker/supervisor_linux.go" -nt "$SUPERVISOR" ] || [ "$ROOT/cmd/microagent-firecracker-supervisor/main_linux.go" -nt "$SUPERVISOR" ]; then
      echo "rebuilding stale cached Firecracker supervisor at $SUPERVISOR" >&2
      go build -buildvcs=false -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
      if ! command -v setcap >/dev/null 2>&1 || ! setcap cap_net_admin,cap_setpcap+ep "$SUPERVISOR" >/dev/null 2>&1; then
        cat >&2 <<EOF
cached Firecracker supervisor was rebuilt and needs file capabilities restored.

Run:
  sudo setcap cap_net_admin,cap_setpcap+ep $SUPERVISOR

Then run:
  scripts/dev/microagent-e2e.sh networking
EOF
        exit 2
      fi
    fi
  elif [ ! -x "$SUPERVISOR" ]; then
    echo "MICROAGENT_FIRECRACKER_SUPERVISOR is not executable: $SUPERVISOR" >&2
    exit 2
  fi
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

if [ "$SUPERVISOR" = "$DEFAULT_SUPERVISOR" ] && caps="$(getcap "$SUPERVISOR" 2>/dev/null)" && [ -n "$caps" ]; then
  echo "temporary supervisor unexpectedly has file capabilities: $caps" >&2
  exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
  if ! supervisor_has_network_caps; then
    cat >&2 <<EOF
microagent E2E networking needs one-time Linux host setup for practical nat and bridged coverage.

Run:
  scripts/dev/microagent-e2e-linux-network-setup.sh

Then run:
  scripts/dev/microagent-e2e.sh networking
EOF
    exit 2
  fi
  if [ -z "$BRIDGE_NAME" ]; then
    BRIDGE_NAME="microagent0"
  fi
  if [ ! -e "/sys/class/net/$BRIDGE_NAME/bridge" ]; then
    echo "microagent E2E bridge $BRIDGE_NAME does not exist; run scripts/dev/microagent-e2e-linux-network-setup.sh" >&2
    exit 2
  fi
  if [ -e /proc/sys/net/ipv4/ip_forward ] && [ "$(cat /proc/sys/net/ipv4/ip_forward)" != "1" ]; then
    echo "net.ipv4.ip_forward must be 1; run scripts/dev/microagent-e2e-linux-network-setup.sh" >&2
    exit 2
  fi
else
  if [ -z "$BRIDGE_NAME" ]; then
    BRIDGE_NAME="brmae2e$((RANDOM % 10000))"
    DELETE_BRIDGE=1
  fi
  if [ -e /proc/sys/net/ipv4/ip_forward ]; then
    ORIGINAL_IP_FORWARD="$(cat /proc/sys/net/ipv4/ip_forward)"
    if [ "$ORIGINAL_IP_FORWARD" != "1" ]; then
      sysctl -w net.ipv4.ip_forward=1 >/dev/null
    fi
  fi
fi

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

"$CLI" doctor >"$STATE_DIR/doctor.json"

ensure_cached_image
seed_image_cache_for_state "$STATE_DIR"

prepare_cached_workspace "$APPLY_WORKSPACE" '{"mode":"isolated"}' '{}' "$STATE_DIR/apply-stopped-create.json"
cat >"$STATE_DIR/apply-stopped.yaml" <<YAML
name: $APPLY_WORKSPACE
restart: always
network:
  mode: user
  forwards:
    - host: 127.0.0.1
      hostPort: $apply_port
      guestPort: 4222
      protocol: tcp
YAML
"$CLI" --json apply --file "$STATE_DIR/apply-stopped.yaml" --state-dir "$STATE_DIR" >"$STATE_DIR/apply-stopped.json"
"$CLI" start "$APPLY_WORKSPACE" --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/apply-stopped-start.json"
wait_for_status_ready "$APPLY_WORKSPACE" "$STATE_DIR/apply-stopped-status-running.json"
"$CLI" network "$APPLY_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/apply-stopped-network.json"
"$CLI" halt "$APPLY_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/apply-stopped-halt.json"
"$CLI" delete "$APPLY_WORKSPACE" --yes --state-dir "$STATE_DIR" >"$STATE_DIR/apply-stopped-delete.json"

if "$CLI" create \
  --id publish-collision \
  --backend firecracker \
  --kernel "$kernel_path" \
  --rootfs "$(cached_image_rootfs_path)" \
  --state-dir "$STATE_DIR/publish-collision" \
  --network user \
  --publish "127.0.0.1:$nats_port:4222/tcp" \
  --publish "127.0.0.1:$nats_port:8222/tcp" >"$STATE_DIR/publish-collision.json" 2>"$STATE_DIR/publish-collision.err"; then
  echo "duplicate published host port unexpectedly succeeded" >&2
  exit 1
fi
grep -qi "duplicate published host port" "$STATE_DIR/publish-collision.err"

if "$CLI" create \
  --id publish-udp \
  --backend firecracker \
  --kernel "$kernel_path" \
  --rootfs "$(cached_image_rootfs_path)" \
  --state-dir "$STATE_DIR/publish-udp" \
  --network user \
  --publish "127.0.0.1:$monitor_port:8222/udp" >"$STATE_DIR/publish-udp.json" 2>"$STATE_DIR/publish-udp.err"; then
  echo "udp published port unexpectedly succeeded" >&2
  exit 1
fi
grep -qi "protocol must be tcp" "$STATE_DIR/publish-udp.err"

if "$CLI" create \
  --id publish-ipv6 \
  --backend firecracker \
  --kernel "$kernel_path" \
  --rootfs "$(cached_image_rootfs_path)" \
  --state-dir "$STATE_DIR/publish-ipv6" \
  --network user \
  --publish "[::1]:$monitor_port:8222/tcp" >"$STATE_DIR/publish-ipv6.json" 2>"$STATE_DIR/publish-ipv6.err"; then
  echo "ipv6 published port unexpectedly succeeded" >&2
  exit 1
fi
grep -qi "publish mapping must be" "$STATE_DIR/publish-ipv6.err"

prepare_cached_workspace "$NAT_WORKSPACE" '{"mode":"nat"}' '{}' "$STATE_DIR/nat-create.json"
"$CLI" start "$NAT_WORKSPACE" --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/nat-start.json"
wait_for_status_ready "$NAT_WORKSPACE" "$STATE_DIR/nat-status-running.json"
"$CLI" connect "$NAT_WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "wget -qO- -T 10 http://example.com >/tmp/nat.out && echo NAT_OUTBOUND_READY || echo NAT_OUTBOUND_FAILED; sync" \
  --ready-timeout 30 \
  --timeout 15 >"$STATE_DIR/nat-connect.txt"
"$CLI" halt "$NAT_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/nat-halt.json"
"$CLI" delete "$NAT_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/nat-delete.json"

python3 - "$STATE_DIR/nat-create.json" "$STATE_DIR/nat-status-running.json" "$STATE_DIR/nat-connect.txt" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    create = json.load(f)
with open(sys.argv[2], "r", encoding="utf-8") as f:
    status = json.load(f)
with open(sys.argv[3], "r", encoding="utf-8", errors="replace") as f:
    console = f.read()
if create.get("response", {}).get("event", {}).get("state") != "prepared":
    raise SystemExit(create)
if status.get("event", {}).get("state") != "running":
    raise SystemExit(status)
runtime = (status.get("network") or {}).get("runtime") or {}
if runtime.get("mode") != "nat":
    raise SystemExit(status.get("network"))
if "NAT_OUTBOUND_READY" not in console:
    raise SystemExit(console)
PY

if [ "$DELETE_BRIDGE" = "1" ]; then
  ip link add "$BRIDGE_NAME" type bridge
fi
if [ "$(id -u)" -eq 0 ]; then
  ip link set "$BRIDGE_NAME" up
fi
prepare_cached_workspace "$BRIDGED_WORKSPACE" "{\"mode\":\"bridged\",\"interface\":\"$BRIDGE_NAME\"}" '{}' "$STATE_DIR/bridged-create.json"
"$CLI" start "$BRIDGED_WORKSPACE" --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/bridged-start.json"
wait_for_status_ready "$BRIDGED_WORKSPACE" "$STATE_DIR/bridged-status-running.json"
"$CLI" connect "$BRIDGED_WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "echo BRIDGED_READY; sync" \
  --ready-timeout 30 \
  --timeout 15 >"$STATE_DIR/bridged-connect.txt"
"$CLI" halt "$BRIDGED_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/bridged-halt.json"
"$CLI" delete "$BRIDGED_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/bridged-delete.json"

python3 - "$STATE_DIR/bridged-create.json" "$STATE_DIR/bridged-status-running.json" "$STATE_DIR/bridged-connect.txt" "$BRIDGE_NAME" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    create = json.load(f)
with open(sys.argv[2], "r", encoding="utf-8") as f:
    status = json.load(f)
with open(sys.argv[3], "r", encoding="utf-8", errors="replace") as f:
    console = f.read()
bridge = sys.argv[4]
if create.get("response", {}).get("event", {}).get("state") != "prepared":
    raise SystemExit(create)
if status.get("event", {}).get("state") != "running":
    raise SystemExit(status)
if "BRIDGED_READY" not in console:
    raise SystemExit(console)
network = create.get("network") or {}
if network.get("mode") != "bridged" or network.get("interface") != bridge:
    raise SystemExit(network)
PY

if "$CLI" create \
  --id bridged-nonbridge \
  --backend firecracker \
  --kernel "$kernel_path" \
  --rootfs "$(cached_image_rootfs_path)" \
  --state-dir "$STATE_DIR/bridged-nonbridge" \
  --network bridged \
  --network-interface lo >"$STATE_DIR/bridged-nonbridge.json" 2>"$STATE_DIR/bridged-nonbridge.err"; then
  echo "bridged nonbridge interface unexpectedly succeeded" >&2
  exit 1
fi
python3 - "$STATE_DIR/bridged-nonbridge.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)
err = result.get("error", result.get("response", {}).get("error", ""))
ok = result.get("ok", result.get("response", {}).get("ok"))
if ok is not False:
    raise SystemExit(result)
if 'bridged network.interface "lo" must be a Linux bridge' not in err:
    raise SystemExit(result)
PY

prepare_cached_workspace "$STATIC_WORKSPACE" '{"mode":"nat","ip":"10.43.240.2/29","subnet":"10.43.240.0/29","gateway":"10.43.240.1","dns":["1.1.1.1","8.8.8.8"],"routes":["0.0.0.0/0 via 10.43.240.1"]}' '{}' "$STATE_DIR/static-create.json"
"$CLI" start "$STATIC_WORKSPACE" --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/static-start.json"
wait_for_status_ready "$STATIC_WORKSPACE" "$STATE_DIR/static-status-running.json"
"$CLI" network "$STATIC_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/static-network-running.json"
"$CLI" connect "$STATIC_WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "grep -q '10.43.240.2' /proc/net/fib_trie; grep -q 'nameserver 1.1.1.1' /etc/resolv.conf; wget -qO- -T 10 http://example.com >/tmp/static-outbound.html; echo STATIC_NET_READY; sync" \
  --ready-timeout 30 \
  --timeout 15 >"$STATE_DIR/static-connect.txt"
"$CLI" halt "$STATIC_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/static-halt.json"
"$CLI" delete "$STATIC_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/static-delete.json"
python3 - "$STATE_DIR/static-create.json" "$STATE_DIR/static-network-running.json" "$STATE_DIR/static-status-running.json" "$STATE_DIR/static-connect.txt" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    create = json.load(f)
with open(sys.argv[2], "r", encoding="utf-8") as f:
    network = json.load(f)
with open(sys.argv[3], "r", encoding="utf-8") as f:
    status = json.load(f)
with open(sys.argv[4], "r", encoding="utf-8", errors="replace") as f:
    console = f.read()

declared = create.get("network") or {}
runtime = network.get("runtime") or {}
status_runtime = (status.get("network") or {}).get("runtime") or {}
for item in (declared, runtime, status_runtime):
    if item.get("mode") != "nat" or item.get("ip") != "10.43.240.2/29" or item.get("gateway") != "10.43.240.1":
        raise SystemExit({"network": network, "status": status, "create": create})
    if item.get("subnet") != "10.43.240.0/29" or item.get("dns") != ["1.1.1.1", "8.8.8.8"]:
        raise SystemExit({"network": network, "status": status, "create": create})
    if item.get("routes") != ["0.0.0.0/0 via 10.43.240.1"]:
        raise SystemExit({"network": network, "status": status, "create": create})
if "STATIC_NET_READY" not in console:
    raise SystemExit(console)
PY

prepare_cached_workspace "$WORKSPACE" "{\"mode\":\"user\",\"port_forwards\":[{\"protocol\":\"tcp\",\"host\":\"127.0.0.1\",\"hostPort\":$nats_port,\"guestPort\":4222},{\"protocol\":\"tcp\",\"host\":\"127.0.0.1\",\"hostPort\":$monitor_port,\"guestPort\":8222}]}" '{"egress":[{"name":"report","path":"/report.json"}]}' "$STATE_DIR/create.json"
write_guest_run_config "$STATE_DIR/run-nats.json" "$nats_port" 4222 "$monitor_port" 8222
"$CLI" cp "$STATE_DIR/run-nats.json" "$WORKSPACE:/etc/microagent/run.json" --state-dir "$STATE_DIR" >"$STATE_DIR/cp-run-config.json"

"$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/start.json"
wait_for_status_ready "$WORKSPACE" "$STATE_DIR/status-running.json"
"$CLI" network "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/network-running.json"

"$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "mkdir -p /data/jetstream; /usr/local/bin/nats-server -js -sd /data/jetstream -m 8222 -a 0.0.0.0 -p 4222 >/tmp/nats.log 2>&1 & wget -qO- -T 10 http://example.com >/tmp/outbound.html && echo E2E_OUTBOUND_READY; printf '{\"ok\":true,\"phase\":\"running\",\"service\":\"nats\"}' > /report.json; sync" \
  --ready-timeout 30 \
  --timeout 15 >"$STATE_DIR/connect-running.txt"

nats_assert monitor "$monitor_port" "$STATE_DIR/monitor-running.json"
nats_assert roundtrip "$nats_port" "$STATE_DIR/nats-roundtrip-running.json"
"$CLI" network "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/network-before-live-apply.json"
cat >"$STATE_DIR/apply-live.yaml" <<YAML
name: $WORKSPACE
network:
  mode: user
  forwards:
    - host: 0.0.0.0
      hostPort: $nats_port
      guestPort: 4222
      protocol: tcp
    - host: 0.0.0.0
      hostPort: $monitor_port
      guestPort: 8222
      protocol: tcp
YAML
"$CLI" --json apply --file "$STATE_DIR/apply-live.yaml" --state-dir "$STATE_DIR" >"$STATE_DIR/apply-live.json"
"$CLI" network "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/network-after-live-apply.json"
nats_assert monitor "$monitor_port" "$STATE_DIR/monitor-after-live-apply.json"
nats_assert roundtrip "$nats_port" "$STATE_DIR/nats-roundtrip-after-live-apply.json"
cat >"$STATE_DIR/apply-live-invalid.yaml" <<YAML
name: $WORKSPACE
network:
  mode: user
  forwards:
    - host: 0.0.0.0
      hostPort: $nats_port
      guestPort: 4223
      protocol: tcp
    - host: 0.0.0.0
      hostPort: $monitor_port
      guestPort: 8222
      protocol: tcp
YAML
if "$CLI" --json apply --file "$STATE_DIR/apply-live-invalid.yaml" --state-dir "$STATE_DIR" >"$STATE_DIR/apply-live-invalid.json" 2>"$STATE_DIR/apply-live-invalid.err"; then
  echo "live apply guest-port change unexpectedly succeeded" >&2
  exit 1
fi
grep -qi "host bind changes" "$STATE_DIR/apply-live-invalid.err"
"$CLI" artifacts "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/artifacts-running.json"
"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt.json"
"$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-halted.json"
mkdir -p "$ARTIFACT_DIR/running"
"$CLI" artifacts get "$WORKSPACE" report "$ARTIFACT_DIR/running" --state-dir "$STATE_DIR" >"$STATE_DIR/artifact-running.json"

"$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/resume.json"
wait_for_status_ready "$WORKSPACE" "$STATE_DIR/status-resumed.json"
"$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "mkdir -p /data/jetstream; /usr/local/bin/nats-server -js -sd /data/jetstream -m 8222 -a 0.0.0.0 -p 4222 >/tmp/nats-resumed.log 2>&1 & wget -qO- -T 10 http://example.com >/tmp/outbound-resumed.html && echo E2E_OUTBOUND_RESUMED; printf '{\"ok\":true,\"phase\":\"resumed\",\"service\":\"nats\"}' > /report.json; sync" \
  --ready-timeout 30 \
  --timeout 15 >"$STATE_DIR/connect-resumed.txt"

nats_assert monitor "$monitor_port" "$STATE_DIR/monitor-resumed.json"
nats_assert roundtrip "$nats_port" "$STATE_DIR/nats-roundtrip-resumed.json"
"$CLI" logs "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/logs.txt"
"$CLI" quarantine "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/quarantine.json"
"$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-quarantined.json"
if "$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/start-quarantined.json" 2>"$STATE_DIR/start-quarantined.err"; then
  echo "start succeeded while microagent workspace was quarantined" >&2
  exit 1
fi
"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt-quarantined.json"
mkdir -p "$ARTIFACT_DIR/resumed"
"$CLI" artifacts get "$WORKSPACE" report "$ARTIFACT_DIR/resumed" --state-dir "$STATE_DIR" >"$STATE_DIR/artifact-resumed.json"
cp "$STATE_DIR/$WORKSPACE/events.json" "$STATE_DIR/events.json"
"$CLI" delete "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/delete.json"

python3 - "$STATE_DIR" "$nats_port" "$monitor_port" "$apply_port" <<'PY'
import json
import os
import sys

state_dir = sys.argv[1]
nats_port = int(sys.argv[2])
monitor_port = int(sys.argv[3])
apply_port = int(sys.argv[4])

def read_json(name):
    with open(os.path.join(state_dir, name), "r", encoding="utf-8") as f:
        return json.load(f)

def read_text(name):
    with open(os.path.join(state_dir, name), "r", encoding="utf-8", errors="replace") as f:
        return f.read()

doctor = read_json("doctor.json")
apply_stopped = read_json("apply-stopped.json")
apply_stopped_status = read_json("apply-stopped-status-running.json")
apply_stopped_network = read_json("apply-stopped-network.json")
apply_stopped_delete = read_json("apply-stopped-delete.json")
create = read_json("create.json")
start = read_json("start.json")
running = read_json("status-running.json")
network = read_json("network-running.json")
network_before_live_apply = read_json("network-before-live-apply.json")
apply_live = read_json("apply-live.json")
network_after_live_apply = read_json("network-after-live-apply.json")
halt = read_json("halt.json")
halted = read_json("status-halted.json")
resume = read_json("resume.json")
resumed = read_json("status-resumed.json")
quarantine = read_json("quarantine.json")
quarantined = read_json("status-quarantined.json")
halt_quarantined = read_json("halt-quarantined.json")
delete = read_json("delete.json")
monitor_running = read_json("monitor-running.json")
monitor_resumed = read_json("monitor-resumed.json")
nats_roundtrip_running = read_json("nats-roundtrip-running.json")
nats_roundtrip_after_live_apply = read_json("nats-roundtrip-after-live-apply.json")
nats_roundtrip_resumed = read_json("nats-roundtrip-resumed.json")
artifact_running = read_json("artifact-running.json")
artifact_resumed = read_json("artifact-resumed.json")

if doctor["ok"] is not True or doctor["backend"] != "firecracker":
    raise SystemExit(doctor)
if doctor["host"]["kvmAvailable"] is not True or doctor["host"]["userNetworkingAvailable"] is not True:
    raise SystemExit(doctor)
if apply_stopped.get("workspace") != "apply-stopped" or set(apply_stopped.get("applied", [])) != {"restart", "network"}:
    raise SystemExit(apply_stopped)
if apply_stopped.get("network", {}).get("mode") != "user":
    raise SystemExit(apply_stopped)
apply_forwards = apply_stopped.get("network", {}).get("portForwards") or apply_stopped.get("network", {}).get("port_forwards") or []
if {(f["hostPort"], f["guestPort"]) for f in apply_forwards} != {(apply_port, 4222)}:
    raise SystemExit(apply_stopped)
if apply_stopped_status.get("event", {}).get("state") != "running":
    raise SystemExit(apply_stopped_status)
if (apply_stopped_network.get("network") or {}).get("mode") != "user":
    raise SystemExit(apply_stopped_network)
if apply_stopped_delete.get("event", {}).get("state") != "stopped":
    raise SystemExit(apply_stopped_delete)
if create["response"]["event"]["state"] != "prepared":
    raise SystemExit(create)
if create["network"]["mode"] != "user":
    raise SystemExit(create["network"])
declared_forwards = create["network"].get("portForwards") or create["network"].get("port_forwards") or []
if {(f["hostPort"], f["guestPort"]) for f in declared_forwards} != {(nats_port, 4222), (monitor_port, 8222)}:
    raise SystemExit(create["network"])
reported_forwards = network["network"].get("portForwards") or network["network"].get("port_forwards") or []
forwards = {(f["hostPort"], f["guestPort"]) for f in reported_forwards}
if (nats_port, 4222) not in forwards or (monitor_port, 8222) not in forwards:
    raise SystemExit(network["network"])
if start["response"]["event"]["state"] != "running" or running["event"]["state"] != "running":
    raise SystemExit(running)
if not running["verification"]["ok"]:
    raise SystemExit(running)
if not running["readiness"]["guestReady"]["ready"] or not running["readiness"]["shellReady"]["ready"]:
    raise SystemExit(running)
if network["network"]["mode"] != "user" or network["runtime"]["mode"] != "user":
    raise SystemExit(network)
if network["runtime"].get("ip", "") == "":
    raise SystemExit(network)
if "E2E_OUTBOUND_READY" not in read_text("connect-running.txt"):
    raise SystemExit(read_text("connect-running.txt"))
if monitor_running.get("jetstream", {}).get("config") is None:
    raise SystemExit(monitor_running)
if not nats_roundtrip_running.get("payload", "").startswith("microagent-nats-roundtrip-"):
    raise SystemExit(nats_roundtrip_running)
before_forwards = (network_before_live_apply.get("network") or {}).get("portForwards") or (network_before_live_apply.get("network") or {}).get("port_forwards") or []
if {f.get("host") for f in before_forwards} != {"127.0.0.1"}:
    raise SystemExit(network_before_live_apply)
if apply_live.get("workspace") != "nats-e2e" or apply_live.get("applied") != ["network"] or apply_live.get("reloaded") is not True:
    raise SystemExit(apply_live)
after_forwards = (network_after_live_apply.get("network") or {}).get("portForwards") or (network_after_live_apply.get("network") or {}).get("port_forwards") or []
if {f.get("host") for f in after_forwards} != {"0.0.0.0"}:
    raise SystemExit(network_after_live_apply)
if not nats_roundtrip_after_live_apply.get("payload", "").startswith("microagent-nats-roundtrip-"):
    raise SystemExit(nats_roundtrip_after_live_apply)
with open(os.path.join(state_dir, "workspaces", "nats-e2e", "workspace.json"), "r", encoding="utf-8") as f:
    live_manifest = json.load(f)
live_forwards = (live_manifest.get("network") or {}).get("port_forwards") or []
if {(f.get("host"), f.get("guestPort")) for f in live_forwards} != {("0.0.0.0", 4222), ("0.0.0.0", 8222)}:
    raise SystemExit(live_manifest)
if halt["event"]["state"] != "halted" or halted["event"]["state"] != "halted":
    raise SystemExit(halted)
if artifact_running["artifact"] != "report" or artifact_running["disk"] != "rootfs":
    raise SystemExit(artifact_running)
with open(os.path.join(state_dir, "artifacts", "running", "report.json"), "r", encoding="utf-8") as f:
    if json.load(f) != {"ok": True, "phase": "running", "service": "nats"}:
        raise SystemExit("running artifact mismatch")
if resume["response"]["event"]["state"] != "running" or resumed["event"]["state"] != "running":
    raise SystemExit(resumed)
if "E2E_OUTBOUND_RESUMED" not in read_text("connect-resumed.txt"):
    raise SystemExit(read_text("connect-resumed.txt"))
if monitor_resumed.get("jetstream", {}).get("config") is None:
    raise SystemExit(monitor_resumed)
if not nats_roundtrip_resumed.get("payload", "").startswith("microagent-nats-roundtrip-"):
    raise SystemExit(nats_roundtrip_resumed)
if artifact_resumed["artifact"] != "report" or artifact_resumed["disk"] != "rootfs":
    raise SystemExit(artifact_resumed)
with open(os.path.join(state_dir, "artifacts", "resumed", "report.json"), "r", encoding="utf-8") as f:
    if json.load(f) != {"ok": True, "phase": "resumed", "service": "nats"}:
        raise SystemExit("resumed artifact mismatch")
if quarantine["event"]["state"] != "quarantined" or quarantined["event"]["state"] != "quarantined":
    raise SystemExit(quarantined)
if halt_quarantined["event"]["state"] != "halted":
    raise SystemExit(halt_quarantined)
if "quarantined" not in read_text("start-quarantined.err"):
    raise SystemExit(read_text("start-quarantined.err"))
with open(os.path.join(state_dir, "events.json"), "r", encoding="utf-8") as f:
    states = [event["state"] for event in json.load(f)]
for expected in ("running", "halted", "quarantined"):
    if expected not in states:
        raise SystemExit(states)
if states.count("running") < 2:
    raise SystemExit(states)
if delete["event"]["state"] != "stopped":
    raise SystemExit(delete)
PY

echo "microagent E2E networking passed"
