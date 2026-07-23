#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-mcp.XXXXXX")"
CLI="$STATE_DIR/microagent"
KEEP_VAR="${MICROAGENT_KEEP_MICROAGENT_E2E_MCP:-0}"

cleanup() {
  status="$?"
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "$KEEP_VAR" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E MCP state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

for required in go python3; do
  e2e_require_cmd "$required" "$required is required for microagent MCP E2E"
done

export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
export GOFLAGS="${GOFLAGS:-} -modcacherw"

(cd "$ROOT" && go build -buildvcs=false -o "$CLI" ./cmd/microagent)

python3 - "$CLI" <<'PY'
import json
import subprocess
import sys

cli = sys.argv[1]
proc = subprocess.Popen(
    [cli, "serve", "mcp"],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
)

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

send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}})
init = recv()
if init.get("result", {}).get("serverInfo", {}).get("name") != "microagent":
    raise SystemExit(init)

send({"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
tools = recv()
tool_by_name = {tool.get("name"): tool for tool in tools.get("result", {}).get("tools", [])}
names = set(tool_by_name)
required = {
    "microagent.ping",
    "microagent.describe",
    "workspace.create",
    "workspace.start",
    "workspace.exec",
    "workspace.events",
    "workspace.stats",
    "snapshot.create",
    "snapshot.list",
    "snapshot.delete",
    "models.pull",
    "models.serve",
    "kernel.install",
    "rootfs.build",
}
missing = sorted(required - names)
if missing:
    raise SystemExit(f"missing MCP tools: {missing}")
for name in ("workspace.create", "workspace.start"):
    properties = tool_by_name[name].get("inputSchema", {}).get("properties", {})
    if "from_snapshot" not in properties:
        raise SystemExit(f"{name} schema missing from_snapshot: {properties}")

send({"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "microagent.ping", "arguments": {}}})
ping = recv()
content = ping.get("result", {}).get("content", [])
if not content or content[0].get("text") != "pong":
    raise SystemExit(ping)

send({"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": {"name": "microagent.describe", "arguments": {}}})
describe = recv()
describe_text = describe.get("result", {}).get("content", [{}])[0].get("text", "")
envelope = json.loads(describe_text)
if envelope.get("ok") is not True or "meta" not in envelope:
    raise SystemExit(envelope)
manifest = envelope.get("result", {})
operations = {op.get("name") for op in manifest.get("operations", [])}
if "workspace.events" not in operations or "workspace.stats" not in operations:
    raise SystemExit(manifest)
if manifest.get("transport") != "mcp_stdio" or manifest.get("output_mode") != "ax":
    raise SystemExit(manifest)

proc.stdin.close()
rc = proc.wait(timeout=5)
if rc != 0:
    stderr = proc.stderr.read().decode("utf-8", errors="replace")
    raise SystemExit(f"MCP server exited {rc}: {stderr}")
PY

echo "microagent E2E MCP stdio passed"
