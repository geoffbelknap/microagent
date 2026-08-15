#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"
SUPERVISOR="${MICROAGENT_APPLEVF_SUPERVISOR:-$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor}"
KERNEL="${MICROAGENT_APPLEVF_KERNEL:-$HOME/.microagent/kernels/apple-vf/arm64/Image}"
if [ ! -r "$KERNEL" ] && [ -r "$HOME/.microagent/kernels/apple-vf/Image" ]; then
  KERNEL="$HOME/.microagent/kernels/apple-vf/Image"
fi
ARCH="${MICROAGENT_APPLEVF_NETWORK_ARCH:-arm64}"
IMAGE="${MICROAGENT_NATS_IMAGE:-docker.io/library/nats:2.10.26-alpine}"
IMAGE_CACHE_STATE="${MICROAGENT_E2E_IMAGE_CACHE_DIR:-$ROOT/.cache/microagent-e2e/image-cache/nats-$ARCH}"
IMAGE_CACHE_POLICY="${MICROAGENT_E2E_IMAGE_CACHE_POLICY:-auto}"
if [ -z "${MICROAGENT_E2E_IMAGE_CACHE_POLICY+x}" ] && [ "${MICROAGENT_E2E_REFRESH_IMAGE_CACHE:-0}" = "1" ]; then
  IMAGE_CACHE_POLICY="refresh"
fi
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-applevf-cached-nats.XXXXXX")"
CLI="$STATE_DIR/microagent"
GUEST_INIT="$STATE_DIR/microagent-guestinit"
WORKSPACE="applevf-cached-nats"
APPLY_WORKSPACE="applevf-apply-stopped"
ARTIFACT_DIR="$STATE_DIR/artifacts"
NATS_SIZE_MIB="${MICROAGENT_APPLEVF_NATS_SIZE_MIB:-256}"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    "$CLI" stop "$WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    if [ "$status" -eq 0 ]; then
      "$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >/dev/null 2>&1 || true
    fi
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_APPLEVF_CACHED_NATS_E2E:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept Apple VF cached NATS E2E state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Darwin:arm64)
    ;;
  *)
    e2e_skip "Apple VF cached NATS E2E requires macOS on Apple silicon"
    ;;
esac

if [ ! -r "$KERNEL" ]; then
  e2e_skip "kernel is not readable at $KERNEL"
fi
if [ ! -x "$SUPERVISOR" ]; then
  e2e_skip "supervisor is not executable at $SUPERVISOR; run scripts/dev/applevf-supervisor-build.sh"
fi
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

case "$IMAGE_CACHE_POLICY" in
  auto|refresh|require)
    ;;
  *)
    e2e_skip "unknown MICROAGENT_E2E_IMAGE_CACHE_POLICY: $IMAGE_CACHE_POLICY"
    ;;
esac

pick_port() {
  python3 - <<'PY'
import socket

with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
}

wait_for_status_ready() {
  workspace="$1"
  output="$2"
  deadline="$((SECONDS + 60))"
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

expect_containment_failure() {
  name="$1"
  expected="$2"
  shift 2
  if "$@" >"$STATE_DIR/${name}.json" 2>"$STATE_DIR/${name}.err"; then
    echo "$name unexpectedly crossed the containment marker" >&2
    exit 1
  fi
  if ! grep -Eqi "$expected" "$STATE_DIR/${name}.json" "$STATE_DIR/${name}.err"; then
    echo "$name failed without the expected containment denial: $expected" >&2
    cat "$STATE_DIR/${name}.json" >&2
    cat "$STATE_DIR/${name}.err" >&2
    exit 1
  fi
}

image_cache_has_ref() {
  cache_state="$1"
  python3 - "$cache_state/images/index.json" "$IMAGE" "$ARCH" <<'PY'
import json
import os
import sys

index_path, image_ref, arch = sys.argv[1:4]
try:
    with open(index_path, "r", encoding="utf-8") as f:
        index = json.load(f)
except FileNotFoundError:
    raise SystemExit(1)
for image in index.get("images", []):
    refs = {image.get("image_ref"), image.get("imageRef"), image.get("resolved_ref"), image.get("resolvedRef"), image.get("digest")}
    if image_ref not in refs:
        continue
    platform = image.get("platform") or {}
    if platform.get("os") not in ("", "linux") or platform.get("architecture") != arch:
        continue
    output_path = image.get("output_path") or image.get("outputPath") or ""
    if output_path and os.path.exists(output_path):
        raise SystemExit(0)
raise SystemExit(1)
PY
}

cached_image_rootfs_path() {
  python3 - "$IMAGE_CACHE_STATE/images/index.json" "$IMAGE" "$ARCH" <<'PY'
import json
import os
import sys

source, image_ref, arch = sys.argv[1:4]
with open(source, "r", encoding="utf-8") as f:
    index = json.load(f)
for image in index.get("images", []):
    refs = {image.get("image_ref"), image.get("imageRef"), image.get("resolved_ref"), image.get("resolvedRef"), image.get("digest")}
    if image_ref not in refs:
        continue
    platform = image.get("platform") or {}
    if platform.get("os") not in ("", "linux") or platform.get("architecture") != arch:
        continue
    output_path = image.get("output_path") or image.get("outputPath") or ""
    if output_path and os.path.exists(output_path):
        print(output_path)
        raise SystemExit(0)
raise SystemExit(f"cached image {image_ref} for {arch} is missing")
PY
}

image_cache_status() {
  if ! image_cache_has_ref "$IMAGE_CACHE_STATE"; then
    printf 'missing\n'
    return 0
  fi
  rootfs_path="$(cached_image_rootfs_path)"
  if [ "$rootfs_path" -nt "$GUEST_INIT" ]; then
    printf 'usable\n'
  else
    printf 'stale-guest-init\n'
  fi
}

ensure_cached_image() {
  mkdir -p "$IMAGE_CACHE_STATE"
  cache_status="$(image_cache_status)"
  case "$IMAGE_CACHE_POLICY:$cache_status" in
    require:missing|require:stale-guest-init)
      cat >&2 <<EOF
Apple VF cached NATS E2E image cache policy is require, but the cache is $cache_status.

Use --image-cache-policy refresh once to rebuild it, or leave the policy at
auto for normal validation runs.
EOF
      exit "$E2E_SKIP_EXIT"
      ;;
    auto:usable|require:usable)
      echo "using cached Apple VF NATS rootfs for $IMAGE" >&2
      return 0
      ;;
    refresh:*|auto:missing|auto:stale-guest-init)
      ;;
    *)
      e2e_skip "unknown MICROAGENT_E2E_IMAGE_CACHE_POLICY: $IMAGE_CACHE_POLICY"
      ;;
  esac
  if [ "$cache_status" = "stale-guest-init" ]; then
    echo "refreshing cached Apple VF NATS rootfs because guest init changed" >&2
  fi
  echo "refreshing cached Apple VF NATS rootfs for $IMAGE" >&2
  "$CLI" image pull "$IMAGE" \
    --state-dir "$IMAGE_CACHE_STATE" \
    --arch "$ARCH" \
    --guest-init "$GUEST_INIT" \
    --mke2fs "$MKE2FS" \
    --size-mib "$NATS_SIZE_MIB" >"$STATE_DIR/image-cache-pull.json"
}

prepare_cached_workspace() {
  name="$1"
  nats_port="$2"
  monitor_port="$3"
  rootfs_source="$(cached_image_rootfs_path)"
  workspace_dir="$STATE_DIR/workspaces/$name"
  runtime_dir="$STATE_DIR/$name"
  mkdir -p "$workspace_dir" "$runtime_dir"
  e2e_copy_workspace_rootfs "$rootfs_source" "$workspace_dir/rootfs.ext4"
  python3 - "$STATE_DIR" "$name" "$nats_port" "$monitor_port" "$NATS_SIZE_MIB" <<'PY'
import json
import os
import sys
import time

state_dir, name, nats_port, monitor_port, size_mib = sys.argv[1:6]
nats_port = int(nats_port)
monitor_port = int(monitor_port)
network = {
    "mode": "user",
    "port_forwards": [
        {"protocol": "tcp", "host": "127.0.0.1", "hostPort": nats_port, "guestPort": 4222},
        {"protocol": "tcp", "host": "127.0.0.1", "hostPort": monitor_port, "guestPort": 8222},
    ],
}
artifacts = {"egress": [{"name": "report", "path": "/report.json"}]}
manifest = {
    "name": name,
    "profile": "small",
    "restart": "never",
    "resources": {"memory_mib": 512, "cpu_count": 2, "size_mib": int(size_mib)},
    "network": network,
    "artifacts": artifacts,
}
now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
event = {
    "identity": {"requestID": f"{name}-prepared", "runtimeID": name, "role": "workload", "backend": "apple-vf"},
    "state": "prepared",
    "detail": "prepared from cached Apple VF NATS rootfs",
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
    "response": {"ok": True, "backend": "apple-vf", "event": event},
}
with open(os.path.join(state_dir, "workspaces", name, "workspace.json"), "w", encoding="utf-8") as f:
    json.dump(manifest, f, indent=2, sort_keys=True)
    f.write("\n")
with open(os.path.join(state_dir, name, "event.json"), "w", encoding="utf-8") as f:
    json.dump(event, f, indent=2, sort_keys=True)
    f.write("\n")
with open(os.path.join(state_dir, "create-cached.json"), "w", encoding="utf-8") as f:
    json.dump(result, f, indent=2, sort_keys=True)
    f.write("\n")
PY
}

nats_assert() {
  mode="$1"
  port="$2"
  output="$3"
  python3 - "$mode" "$port" "$output" <<'PY'
import http.client
import json
import socket
import sys
import time

mode, port_raw, output = sys.argv[1:4]
port = int(port_raw)
deadline = time.time() + 25
last_error = ""

def nats_roundtrip():
    payload = f"microagent-applevf-nats-roundtrip-{time.time_ns()}"
    with socket.create_connection(("127.0.0.1", port), timeout=2) as sock:
        sock.settimeout(2)
        sock.recv(4096)
        sock.sendall(b"CONNECT {\"verbose\":false,\"pedantic\":false}\r\n")
        sock.sendall(b"SUB microagent.e2e 1\r\n")
        sock.sendall(b"PUB microagent.e2e " + str(len(payload)).encode() + b"\r\n" + payload.encode() + b"\r\n")
        sock.sendall(b"PING\r\n")
        data = b""
        while time.time() < deadline:
            chunk = sock.recv(4096)
            if not chunk:
                break
            data += chunk
            if payload.encode() in data and b"PONG" in data:
                with open(output, "w", encoding="utf-8") as f:
                    json.dump({"payload": payload, "response": data.decode("utf-8", errors="replace")}, f, indent=2, sort_keys=True)
                    f.write("\n")
                return
        raise RuntimeError(data.decode("utf-8", errors="replace"))

def monitor_probe():
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=2)
    conn.request("GET", "/varz")
    resp = conn.getresponse()
    body = resp.read()
    if resp.status != 200:
        raise RuntimeError(f"status {resp.status}: {body!r}")
    data = json.loads(body.decode("utf-8"))
    if "server_id" not in data:
        raise RuntimeError(data)
    with open(output, "w", encoding="utf-8") as f:
        json.dump(data, f, indent=2, sort_keys=True)
        f.write("\n")

while time.time() < deadline:
    try:
        if mode == "roundtrip":
            nats_roundtrip()
        elif mode == "monitor":
            monitor_probe()
        else:
            raise RuntimeError(f"unknown mode {mode}")
        raise SystemExit(0)
    except Exception as err:
        last_error = str(err)
        time.sleep(0.5)
raise SystemExit(f"NATS {mode} probe failed: {last_error}")
PY
}

(
  cd "$ROOT"
  go build -buildvcs=false -o "$CLI" ./cmd/microagent
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

export MICROAGENT_APPLEVF_SUPERVISOR="$SUPERVISOR"

nats_port="$(pick_port)"
monitor_port="$(pick_port)"
apply_port="$(pick_port)"

"$CLI" doctor --backend apple-vf --arch "$ARCH" --supervisor "$SUPERVISOR" >"$STATE_DIR/doctor.json"
ensure_cached_image

prepare_cached_workspace "$APPLY_WORKSPACE" "$apply_port" "$monitor_port"
cat >"$STATE_DIR/apply-stopped.yaml" <<YAML
name: $APPLY_WORKSPACE
restart: always
network:
  mode: user
  forwards:
    - host: 0.0.0.0
      hostPort: $apply_port
      guestPort: 4222
      protocol: tcp
YAML
"$CLI" --json apply \
  --file "$STATE_DIR/apply-stopped.yaml" \
  --state-dir "$STATE_DIR" \
  --backend apple-vf \
  --supervisor "$SUPERVISOR" \
  --arch "$ARCH" >"$STATE_DIR/apply-stopped.json"
"$CLI" start "$APPLY_WORKSPACE" --state-dir "$STATE_DIR" --kernel "$KERNEL" --supervisor "$SUPERVISOR" >"$STATE_DIR/apply-stopped-start.json"
wait_for_status_ready "$APPLY_WORKSPACE" "$STATE_DIR/apply-stopped-status-running.json"
"$CLI" network "$APPLY_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/apply-stopped-network.json"
"$CLI" halt "$APPLY_WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/apply-stopped-halt.json"
"$CLI" delete "$APPLY_WORKSPACE" --yes --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/apply-stopped-delete.json"

prepare_cached_workspace "$WORKSPACE" "$nats_port" "$monitor_port"

"$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$KERNEL" --supervisor "$SUPERVISOR" >"$STATE_DIR/start.json"
wait_for_status_ready "$WORKSPACE" "$STATE_DIR/status-running.json"
"$CLI" network "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/network-running.json"
"$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "mkdir -p /data/jetstream; setsid /usr/local/bin/nats-server -js -sd /data/jetstream -m 8222 -a 0.0.0.0 -p 4222 </dev/null >/tmp/nats.log 2>&1 & wget -qO- -T 10 http://example.com >/tmp/outbound.html && echo APPLEVF_NATS_OUTBOUND_READY; printf '{\"ok\":true,\"phase\":\"running\",\"service\":\"nats\"}' > /report.json; sync" \
  --ready-timeout 30 \
  --timeout 15 >"$STATE_DIR/connect-running.txt"
nats_assert monitor "$monitor_port" "$STATE_DIR/monitor-running.json"
nats_assert roundtrip "$nats_port" "$STATE_DIR/nats-roundtrip-running.json"
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
"$CLI" --json apply \
  --file "$STATE_DIR/apply-live.yaml" \
  --state-dir "$STATE_DIR" \
  --backend apple-vf \
  --supervisor "$SUPERVISOR" \
  --arch "$ARCH" >"$STATE_DIR/apply-live.json"
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
if "$CLI" --json apply \
  --file "$STATE_DIR/apply-live-invalid.yaml" \
  --state-dir "$STATE_DIR" \
  --backend apple-vf \
  --supervisor "$SUPERVISOR" \
  --arch "$ARCH" >"$STATE_DIR/apply-live-invalid.json" 2>"$STATE_DIR/apply-live-invalid.err"; then
  echo "Apple VF live apply guest-port change unexpectedly succeeded" >&2
  exit 1
fi
grep -qi "host bind changes" "$STATE_DIR/apply-live-invalid.err"
"$CLI" artifact "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/artifacts-running.json"
"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" >"$STATE_DIR/halt.json"
"$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-halted.json"
mkdir -p "$ARTIFACT_DIR/running"
"$CLI" artifact get "$WORKSPACE" report "$ARTIFACT_DIR/running" \
  --state-dir "$STATE_DIR" \
  --debugfs "$DEBUGFS" >"$STATE_DIR/artifact-running.json"

"$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$KERNEL" --supervisor "$SUPERVISOR" >"$STATE_DIR/resume.json"
wait_for_status_ready "$WORKSPACE" "$STATE_DIR/status-resumed.json"
"$CLI" connect "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "mkdir -p /data/jetstream; setsid /usr/local/bin/nats-server -js -sd /data/jetstream -m 8222 -a 0.0.0.0 -p 4222 </dev/null >/tmp/nats-resumed.log 2>&1 & wget -qO- -T 10 http://example.com >/tmp/outbound-resumed.html && echo APPLEVF_NATS_OUTBOUND_RESUMED; printf '{\"ok\":true,\"phase\":\"resumed\",\"service\":\"nats\"}' > /report.json; sync" \
  --ready-timeout 30 \
  --timeout 15 >"$STATE_DIR/connect-resumed.txt"
nats_assert monitor "$monitor_port" "$STATE_DIR/monitor-resumed.json"
nats_assert roundtrip "$nats_port" "$STATE_DIR/nats-roundtrip-resumed.json"

"$CLI" quarantine "$WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR" --reason "cached NATS E2E quarantine" --yes >"$STATE_DIR/quarantine.json"
python3 - "$nats_port" "$monitor_port" <<'PY'
import socket
import sys
import time

ports = [int(item) for item in sys.argv[1:]]
deadline = time.time() + 5
while time.time() < deadline:
    open_ports = []
    for port in ports:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.5):
                open_ports.append(port)
        except OSError:
            pass
    if not open_ports:
        raise SystemExit(0)
    time.sleep(0.2)
raise SystemExit(f"published NATS listeners stayed open after quarantine: {open_ports}")
PY
expect_containment_failure halt-quarantined "containment marker" \
  "$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR"
expect_containment_failure delete-contained "containment|custody" \
  "$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" --supervisor "$SUPERVISOR"

python3 - "$STATE_DIR" "$nats_port" "$monitor_port" "$apply_port" <<'PY'
import json
import os
import sys

state_dir, nats_port, monitor_port, apply_port = sys.argv[1:5]
nats_port = int(nats_port)
monitor_port = int(monitor_port)
apply_port = int(apply_port)

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
create = read_json("create-cached.json")
start = read_json("start.json")
running = read_json("status-running.json")
network = read_json("network-running.json")
apply_live = read_json("apply-live.json")
network_after_live_apply = read_json("network-after-live-apply.json")
halt = read_json("halt.json")
halted = read_json("status-halted.json")
resume = read_json("resume.json")
resumed = read_json("status-resumed.json")
quarantine = read_json("quarantine.json")
halt_quarantined = read_json("halt-quarantined.json")
delete_contained = read_json("delete-contained.json")
roundtrip_running = read_json("nats-roundtrip-running.json")
roundtrip_after_live_apply = read_json("nats-roundtrip-after-live-apply.json")
roundtrip_resumed = read_json("nats-roundtrip-resumed.json")
monitor_after_live_apply = read_json("monitor-after-live-apply.json")
monitor_resumed = read_json("monitor-resumed.json")
artifact_running = read_json("artifact-running.json")

if doctor.get("ok") is not True or doctor.get("backend") != "apple-vf":
    raise SystemExit(doctor)
if apply_stopped.get("workspace") != "applevf-apply-stopped" or set(apply_stopped.get("applied", [])) != {"restart", "network"}:
    raise SystemExit(apply_stopped)
if apply_stopped.get("network", {}).get("mode") != "user":
    raise SystemExit(apply_stopped)
apply_forwards = apply_stopped.get("network", {}).get("portForwards") or apply_stopped.get("network", {}).get("port_forwards") or []
if {(item.get("host"), item.get("hostPort"), item.get("guestPort")) for item in apply_forwards} != {("0.0.0.0", apply_port, 4222)}:
    raise SystemExit(apply_stopped)
if apply_stopped_status.get("event", {}).get("state") != "running":
    raise SystemExit(apply_stopped_status)
if (apply_stopped_network.get("network") or {}).get("mode") != "user":
    raise SystemExit(apply_stopped_network)
if apply_stopped_delete.get("event", {}).get("state") != "stopped":
    raise SystemExit(apply_stopped_delete)
if create.get("response", {}).get("event", {}).get("state") != "prepared":
    raise SystemExit(create)
if start.get("response", {}).get("event", {}).get("state") != "running":
    raise SystemExit(start)
if running.get("event", {}).get("state") != "running":
    raise SystemExit(running)
for body in (create, running, network):
    cfg = body.get("network") or {}
    if cfg.get("mode") != "user":
        raise SystemExit(body)
    forwards = cfg.get("portForwards") or cfg.get("port_forwards") or []
    ports = {(item.get("hostPort"), item.get("guestPort")) for item in forwards}
    if (nats_port, 4222) not in ports or (monitor_port, 8222) not in ports:
        raise SystemExit(body)
if "APPLEVF_NATS_OUTBOUND_READY" not in read_text("connect-running.txt"):
    raise SystemExit(read_text("connect-running.txt"))
if "APPLEVF_NATS_OUTBOUND_RESUMED" not in read_text("connect-resumed.txt"):
    raise SystemExit(read_text("connect-resumed.txt"))
if not roundtrip_running.get("payload", "").startswith("microagent-applevf-nats-roundtrip-"):
    raise SystemExit(roundtrip_running)
if apply_live.get("workspace") != "applevf-cached-nats" or apply_live.get("applied") != ["network"] or apply_live.get("reloaded") is not True:
    raise SystemExit(apply_live)
after_forwards = (network_after_live_apply.get("network") or {}).get("portForwards") or (network_after_live_apply.get("network") or {}).get("port_forwards") or []
if {(item.get("host"), item.get("hostPort"), item.get("guestPort")) for item in after_forwards} != {("0.0.0.0", nats_port, 4222), ("0.0.0.0", monitor_port, 8222)}:
    raise SystemExit(network_after_live_apply)
if not roundtrip_after_live_apply.get("payload", "").startswith("microagent-applevf-nats-roundtrip-"):
    raise SystemExit(roundtrip_after_live_apply)
if monitor_after_live_apply.get("jetstream", {}).get("config") is None:
    raise SystemExit(monitor_after_live_apply)
if not roundtrip_resumed.get("payload", "").startswith("microagent-applevf-nats-roundtrip-"):
    raise SystemExit(roundtrip_resumed)
if monitor_resumed.get("jetstream", {}).get("config") is None:
    raise SystemExit(monitor_resumed)
if halt.get("event", {}).get("state") != "halted" or halted.get("event", {}).get("state") != "halted":
    raise SystemExit(halted)
if resume.get("response", {}).get("event", {}).get("state") != "running" or resumed.get("event", {}).get("state") != "running":
    raise SystemExit(resumed)
if quarantine.get("event", {}).get("state") != "quarantined":
    raise SystemExit(quarantine)
if halt_quarantined.get("ok") is not False or "containment marker" not in halt_quarantined.get("error", ""):
    raise SystemExit(halt_quarantined)
if delete_contained.get("ok") is not False or not any(word in delete_contained.get("error", "") for word in ("containment", "custody")):
    raise SystemExit(delete_contained)
if artifact_running.get("artifact") != "report":
    raise SystemExit(artifact_running)
with open(os.path.join(state_dir, "artifacts", "running", "report.json"), "r", encoding="utf-8") as f:
    if json.load(f) != {"ok": True, "phase": "running", "service": "nats"}:
        raise SystemExit("running artifact mismatch")
PY

echo "Apple VF cached NATS E2E passed"
