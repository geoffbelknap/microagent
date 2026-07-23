#!/usr/bin/env bash
set -euo pipefail

# MCP-driven workspace lifecycle E2E: boots `serve mcp` from a dev build and
# drives create/start/exec/kill/delete through JSON-RPC tool calls against a
# real microVM, then runs the identical lifecycle through the CLI and asserts
# the two surfaces report the same states and exec results. Agents are a
# first-class consumer of the MCP adapter; this scenario is what keeps new
# lifecycle features honest there, not just on the CLI.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-mcp-lifecycle.XXXXXX")"
CLI="$STATE_DIR/microagent"
KEEP_VAR="${MICROAGENT_KEEP_MICROAGENT_E2E_MCP_LIFECYCLE:-0}"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    for ws in mcp-lc cli-lc; do
      "$CLI" delete "$ws" --yes --force --state-dir "$STATE_DIR/ws" >/dev/null 2>&1 || true
    done
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "$KEEP_VAR" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E MCP lifecycle state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

for required in go python3; do
  e2e_require_cmd "$required" "$required is required for microagent MCP lifecycle E2E"
done
e2e_require_vm

# NETWORK_MODE threads into both the MCP and CLI create calls when set.
# Linux/macOS keep the backend default (empty); windows-hyperv pins isolated
# because the default user/nat modes need HNS elevation on Windows hosts.
NETWORK_MODE=""
case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ARCH=amd64
    KERNEL_BACKEND=linux-kvm
    IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f}"
    MICROAGENT_FIRECRACKER="$(e2e_resolve_firecracker)"
    export MICROAGENT_FIRECRACKER
    ;;
  Darwin:arm64)
    ARCH=arm64
    KERNEL_BACKEND=apple-vf
    IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox@sha256:bd44eb136a95dcc8dc58995e43abc40a413f2e8e3d4a2aae6bccbe94686acb05}"
    SUPERVISOR="${MICROAGENT_APPLEVF_SUPERVISOR:-$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor}"
    if [ ! -x "$SUPERVISOR" ]; then
      e2e_skip "Apple VF supervisor is not executable at $SUPERVISOR; run scripts/dev/applevf-supervisor-build.sh"
    fi
    export MICROAGENT_APPLEVF_SUPERVISOR="$SUPERVISOR"
    ;;
  MINGW*:x86_64|MSYS*:x86_64|CYGWIN*:x86_64)
    e2e_have_hcs || e2e_skip "Hyper-V HCS services (vmms/vmcompute) are not running"
    ARCH=amd64
    KERNEL_BACKEND=windows-hyperv
    IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f}"
    # The CLI must be the .exe so os.Executable-based helpers resolve; the
    # guest init built next to it ($STATE_DIR/microagent-guestinit-amd64) is
    # found by the default sibling resolution, so the MCP create needs no
    # guest-init plumbing. The in-process supervisor takes no path. The tiny
    # profile's 512 MiB rootfs covers the busybox VHD build headroom.
    CLI="$STATE_DIR/microagent.exe"
    NETWORK_MODE="isolated"
    ;;
  *)
    e2e_skip "MCP lifecycle E2E requires Linux amd64, macOS arm64, or Windows amd64"
    ;;
esac

export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
export GOFLAGS="${GOFLAGS:-} -modcacherw"

e2e_step "build dev CLI, supervisor, and guest init"
(
  cd "$ROOT"
  go build -buildvcs=false -o "$CLI" ./cmd/microagent
  if [ "$KERNEL_BACKEND" = "linux-kvm" ]; then
    go build -buildvcs=false -o "$STATE_DIR/microagent-firecracker-supervisor" ./cmd/microagent-firecracker-supervisor
  fi
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -buildvcs=false -o "$STATE_DIR/microagent-guestinit-$ARCH" ./cmd/microagent-guestinit
)
if [ "$KERNEL_BACKEND" = "linux-kvm" ]; then
  export MICROAGENT_FIRECRACKER_SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
fi

e2e_step "install kernel"
"$CLI" kernel install --backend "$KERNEL_BACKEND" --arch "$ARCH" >"$STATE_DIR/kernel-install.json"

e2e_step "drive workspace lifecycle over MCP stdio and assert CLI parity"
# Host-native path forms: python spawns the CLI directly (no Git Bash arg
# conversion), so on Windows the CLI and state paths must already be in
# Windows form. e2e_host_path is the identity off Windows.
python3 - "$(e2e_host_path "$CLI")" "$(e2e_host_path "$STATE_DIR")" "$NETWORK_MODE" "$IMAGE" <<'PY'
import base64
import json
import subprocess
import sys

cli = sys.argv[1]
state_root = sys.argv[2]
network_mode = sys.argv[3] if len(sys.argv) > 3 else ""
ws_state = state_root + "/ws"
IMAGE = sys.argv[4]

proc = subprocess.Popen(
    [cli, "serve", "mcp"],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
)

request_id = 0


def send(message):
    body = json.dumps(message, separators=(",", ":")).encode("utf-8")
    proc.stdin.write(b"Content-Length: " + str(len(body)).encode("ascii") + b"\r\n\r\n" + body)
    proc.stdin.flush()


def recv():
    headers = {}
    while True:
        line = proc.stdout.readline()
        if line == b"":
            stderr = proc.stderr.read().decode("utf-8", errors="replace")
            raise SystemExit(f"MCP server closed stdout early; stderr={stderr}")
        line = line.decode("ascii").strip()
        if line == "":
            break
        name, value = line.split(":", 1)
        headers[name.lower()] = value.strip()
    length = int(headers["content-length"])
    return json.loads(proc.stdout.read(length).decode("utf-8"))


def call(tool, arguments, timeout_note=""):
    global request_id
    request_id += 1
    send({
        "jsonrpc": "2.0",
        "id": request_id,
        "method": "tools/call",
        "params": {"name": tool, "arguments": arguments},
    })
    reply = recv()
    if "error" in reply:
        raise SystemExit(f"{tool} JSON-RPC error: {reply}")
    result = reply.get("result", {})
    text = result.get("content", [{}])[0].get("text", "")
    try:
        envelope = json.loads(text)
    except json.JSONDecodeError:
        raise SystemExit(f"{tool} returned non-JSON content: {text!r}")
    if result.get("isError"):
        raise SystemExit(f"{tool} tool error: {envelope}")
    return envelope.get("result", envelope)


def find_state(value):
    # Lifecycle envelopes carry the state under event.state (control verbs),
    # response.event.state (raw create), or a flattened top-level "state" (the
    # compact create summary). Search all without pinning the wrapper.
    if isinstance(value, dict):
        event = value.get("event")
        if isinstance(event, dict) and "state" in event:
            return event["state"]
        if isinstance(value.get("state"), str):
            return value["state"]
        for key in ("response", "result"):
            nested = value.get(key)
            found = find_state(nested)
            if found is not None:
                return found
    return None


def cli_call(args):
    out = subprocess.run([cli, "--mode=ax", *args], capture_output=True, text=True)
    if out.returncode != 0:
        raise SystemExit(f"cli {' '.join(args)} failed rc={out.returncode}: {out.stderr}")
    parsed = json.loads(out.stdout)
    # AX responses are one {ok, result|error} envelope; unwrap to the result so
    # direct-CLI parsing matches the pre-envelope (and MCP) shape.
    if isinstance(parsed, dict) and "ok" in parsed and "result" in parsed:
        return parsed["result"]
    return parsed


def exec_summary(result):
    exec_result = result.get("result", result) if isinstance(result, dict) else {}
    if "exit_code" not in exec_result:
        # CLI exec envelope may nest the protocol result one level down.
        for value in (result or {}).values():
            if isinstance(value, dict) and "exit_code" in value:
                exec_result = value
                break
    stdout = exec_result.get("stdout", "")
    if isinstance(stdout, str):
        try:
            stdout = base64.b64decode(stdout).decode("utf-8", errors="replace")
        except Exception:
            pass
    return exec_result.get("exit_code"), stdout.strip()


def wait_exec_ready(inspect, name, attempts=90):
    import time
    for _ in range(attempts):
        status = inspect(name)
        readiness = status.get("readiness") or {}
        exec_ready = (readiness.get("execReady") or {}).get("ready")
        if exec_ready:
            return status
        time.sleep(1)
    raise SystemExit(f"workspace {name} exec service never became ready: {status}")


send({"jsonrpc": "2.0", "id": 0, "method": "initialize", "params": {}})
init = recv()
if init.get("result", {}).get("serverInfo", {}).get("name") != "microagent":
    raise SystemExit(init)

# --- MCP-driven lifecycle ---
mcp = {}
create_args = {"name": "mcp-lc", "image": IMAGE, "profile": "tiny", "state_dir": ws_state}
if network_mode:
    create_args["network"] = network_mode
create = call("workspace.create", create_args)
mcp["create"] = find_state(create)

start = call("workspace.start", {"name": "mcp-lc", "state_dir": ws_state})
mcp["start"] = find_state(start)

wait_exec_ready(lambda n: call("workspace.inspect", {"name": n, "state_dir": ws_state, "format": "full"}), "mcp-lc")
inspect = call("workspace.inspect", {"name": "mcp-lc", "state_dir": ws_state, "format": "full"})
mcp["inspect"] = find_state(inspect)

exec_result = call("workspace.exec", {"name": "mcp-lc", "argv": ["echo", "lifecycle-parity"], "state_dir": ws_state})
mcp["exec_code"], mcp["exec_stdout"] = exec_summary(exec_result)

kill = call("workspace.kill", {"name": "mcp-lc", "state_dir": ws_state})
mcp["kill"] = find_state(kill)

delete = call("workspace.delete", {"name": "mcp-lc", "state_dir": ws_state})
mcp["delete"] = find_state(delete)

listing = call("workspace.list", {"state_dir": ws_state})
mcp["listed"] = [w.get("name") for w in (listing.get("workspaces") or [])]

# --- identical lifecycle through the CLI ---
cli_states = {}
cli_create = ["create", "cli-lc", "--image", IMAGE, "--profile", "tiny", "--state-dir", ws_state]
if network_mode:
    cli_create += ["--network", network_mode]
create = cli_call(cli_create)
cli_states["create"] = find_state(create)

start = cli_call(["start", "cli-lc", "--state-dir", ws_state])
cli_states["start"] = find_state(start)

wait_exec_ready(lambda n: cli_call(["status", n, "--state-dir", ws_state]), "cli-lc")
status = cli_call(["status", "cli-lc", "--state-dir", ws_state])
cli_states["inspect"] = find_state(status)

exec_result = cli_call(["exec", "cli-lc", "--state-dir", ws_state, "--", "echo", "lifecycle-parity"])
cli_states["exec_code"], cli_states["exec_stdout"] = exec_summary(exec_result)

kill = cli_call(["kill", "cli-lc", "--state-dir", ws_state])
cli_states["kill"] = find_state(kill)

delete = cli_call(["delete", "cli-lc", "--yes", "--state-dir", ws_state])
cli_states["delete"] = find_state(delete)

listing = cli_call(["list", "--state-dir", ws_state])
cli_states["listed"] = [w.get("name") for w in (listing.get("workspaces") or [])]

# --- parity assertions ---
expected = {
    "create": ("prepared", "stopped"),
    "start": ("running",),
    "inspect": ("running",),
    "kill": ("stopped",),
    "delete": ("stopped",),
}
for step, allowed in expected.items():
    if mcp[step] not in allowed:
        raise SystemExit(f"MCP {step} state = {mcp[step]!r}, want one of {allowed}; mcp={mcp}")
    if cli_states[step] != mcp[step]:
        raise SystemExit(f"parity: {step} state MCP={mcp[step]!r} CLI={cli_states[step]!r}")

if mcp["exec_code"] != 0 or mcp["exec_stdout"] != "lifecycle-parity":
    raise SystemExit(f"MCP exec result = {mcp['exec_code']!r}/{mcp['exec_stdout']!r}")
if (mcp["exec_code"], mcp["exec_stdout"]) != (cli_states["exec_code"], cli_states["exec_stdout"]):
    raise SystemExit(f"parity: exec MCP={mcp['exec_code']}/{mcp['exec_stdout']!r} CLI={cli_states['exec_code']}/{cli_states['exec_stdout']!r}")

if mcp["listed"] != [] or cli_states["listed"] != []:
    raise SystemExit(f"workspaces remain after delete: mcp={mcp['listed']} cli={cli_states['listed']}")

proc.stdin.close()
rc = proc.wait(timeout=10)
if rc != 0:
    stderr = proc.stderr.read().decode("utf-8", errors="replace")
    raise SystemExit(f"MCP server exited {rc}: {stderr}")

print(f"MCP/CLI lifecycle parity: {json.dumps(mcp, sort_keys=True)}")
PY

echo "microagent E2E MCP lifecycle passed"
