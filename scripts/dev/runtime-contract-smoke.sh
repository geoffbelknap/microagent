#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-runtime-contract.XXXXXX")"
CLI="$STATE_DIR/microagent"
DEBUGFS="$STATE_DIR/debugfs"
WORKSPACE="contract-smoke"
case "$(uname -s)" in
  Darwin)
    HOST_BACKEND="apple-vf"
    ;;
  Linux)
    HOST_BACKEND="firecracker"
    ;;
  *)
    echo "runtime contract smoke requires macOS or Linux" >&2
    exit 1
    ;;
esac

export GOCACHE="${GOCACHE:-$ROOT/.cache/go-build}"
export GOMODCACHE="${GOMODCACHE:-$ROOT/.cache/gomodcache}"
mkdir -p "$GOCACHE" "$GOMODCACHE"

cleanup() {
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  rm -rf "$STATE_DIR"
}
trap cleanup EXIT

(
  cd "$ROOT"
  build_output="$(go build -buildvcs=false -o "$CLI" ./cmd/microagent 2>&1)" || {
    printf '%s\n' "$build_output" >&2
    exit 1
  }
  if [ -n "$build_output" ]; then
    printf '%s\n' "$build_output" | grep -v '^go: writing stat cache:' >&2 || true
  fi
)

cat >"$DEBUGFS" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
args=("$@")
for ((i=0; i<${#args[@]}; i++)); do
  if [[ "${args[$i]}" == "-R" ]]; then
    command="${args[$((i+1))]}"
    if [[ "$command" == dump\ * ]]; then
      target="${command##* }"
      printf '{"artifact":"report","ok":true}\n' >"$target"
    fi
  fi
done
SH
chmod +x "$DEBUGFS"

"$CLI" --json contract >"$STATE_DIR/contract.json"

python3 - "$STATE_DIR" "$WORKSPACE" "$HOST_BACKEND" <<'PY'
import datetime
import hashlib
import json
import os
import sys

state_dir, workspace, host_backend = sys.argv[1:]
runtime_dir = os.path.join(state_dir, workspace)
workspace_dir = os.path.join(state_dir, "workspaces", workspace)
disk_dir = os.path.join(workspace_dir, "disks")
os.makedirs(runtime_dir, exist_ok=True)
os.makedirs(disk_dir, exist_ok=True)

kernel = os.path.join(state_dir, "Image")
rootfs = os.path.join(workspace_dir, "rootfs.ext4")
init = os.path.join(state_dir, "microagent-init")
disk = os.path.join(disk_dir, "workspace.ext4")
for path, data in (
    (kernel, b"kernel"),
    (rootfs, b"rootfs"),
    (init, b"init"),
    (disk, b"disk"),
):
    with open(path, "wb") as f:
        f.write(data)

def sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        h.update(f.read())
    return h.hexdigest()

observed = datetime.datetime.now(datetime.UTC).isoformat().replace("+00:00", "Z")
identity = {
    "requestID": "req-contract-smoke",
    "runtimeID": workspace,
    "role": "workload",
    "backend": host_backend,
}
event = {
    "identity": identity,
    "state": "halted",
    "detail": "serial=" + os.path.join(runtime_dir, "serial.log"),
    "observedAt": observed,
}
manifest = {
    "name": workspace,
    "profile": "small",
    "restart": "never",
    "resources": {"memory_mib": 512, "cpu_count": 2, "size_mib": 1024},
    "network": {"mode": "nat"},
    "mediation": {
        "enabled": True,
        "required": True,
        "port": 2048,
        "target": "127.0.0.1:9900",
        "failClosed": True,
    },
    "disks": [
        {
            "name": "workspace",
            "path": disk,
            "mountpoint": "/workspace",
            "mode": "rw",
        },
        {
            "name": "config",
            "source_path": os.path.join(state_dir, "config.tar"),
            "path": os.path.join(disk_dir, "config.ext4"),
            "mountpoint": "/config",
            "mode": "ro",
            "bundle": True,
        },
    ],
    "artifacts": {
        "ingress": [
            {
                "name": "config",
                "source_path": os.path.join(state_dir, "config.tar"),
                "path": os.path.join(disk_dir, "config.ext4"),
                "mountpoint": "/config",
                "mode": "ro",
                "bundle": True,
            }
        ],
        "egress": [{"name": "report", "path": "/workspace/report.json"}],
    },
    "verification": {
        "ok": True,
        "imageRef": "docker.io/library/busybox:1.36",
        "resolvedRef": "docker.io/library/busybox@sha256:contract",
        "imageDigest": "sha256:contract",
        "kernel": {"path": kernel, "sha256": sha256(kernel)},
        "rootfs": {"path": rootfs, "sha256": sha256(rootfs)},
        "init": {"path": init, "sha256": sha256(init)},
    },
}
runtime = {
    "event": event,
    "config": {
        "kernelPath": kernel,
        "rootfsPath": rootfs,
        "stateDir": state_dir,
        "memoryMiB": 512,
        "cpuCount": 2,
        "disks": [{"name": "workspace", "path": disk, "mountpoint": "/workspace", "mode": "rw"}],
        "mediation": manifest["mediation"],
        "network": {"mode": "nat"},
        "serialInput": True,
    },
    "serialLogPath": os.path.join(runtime_dir, "serial.log"),
    "serialInputPath": os.path.join(runtime_dir, "serial.in"),
    "startedAt": observed,
    "updatedAt": observed,
}
guest_result = {
    "started_at": observed,
    "exited_at": observed,
    "exit_code": 0,
    "stdout": "contract smoke\n",
}
files = {
    os.path.join(workspace_dir, "workspace.json"): manifest,
    os.path.join(runtime_dir, "event.json"): event,
    os.path.join(runtime_dir, "events.json"): [event],
    os.path.join(runtime_dir, "runtime.json"): runtime,
    os.path.join(runtime_dir, "result.json"): guest_result,
}
for path, body in files.items():
    with open(path, "w", encoding="utf-8") as f:
        json.dump(body, f, indent=2)
        f.write("\n")
with open(os.path.join(runtime_dir, "serial.log"), "w", encoding="utf-8") as f:
    f.write("contract smoke serial\n")
PY

python3 - "$STATE_DIR/contract.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    contract = json.load(f)
assert contract["version"] == "agent-runtime.v1"
assert {"firecracker", "apple-vf"} <= set(contract["backends"])
assert "quarantine" in {item["name"] for item in contract["commands"]}
assert "mediationReady" in {item["name"] for item in contract["readinessSignals"]}
assert "egress" in {item["name"] for item in contract["artifactChannels"]}
PY

"$CLI" status "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status.json"
"$CLI" result "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/result.json"
"$CLI" artifacts "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/artifacts.json"
mkdir -p "$STATE_DIR/out"
"$CLI" artifacts get "$WORKSPACE" report "$STATE_DIR/out" --state-dir "$STATE_DIR" --debugfs "$DEBUGFS" >"$STATE_DIR/artifact-get.json"

python3 - "$STATE_DIR" "$WORKSPACE" <<'PY'
import json
import os
import sys

state_dir, workspace = sys.argv[1:]
with open(os.path.join(state_dir, "status.json"), "r", encoding="utf-8") as f:
    status = json.load(f)
with open(os.path.join(state_dir, "result.json"), "r", encoding="utf-8") as f:
    result = json.load(f)
with open(os.path.join(state_dir, "artifacts.json"), "r", encoding="utf-8") as f:
    artifacts = json.load(f)
with open(os.path.join(state_dir, "artifact-get.json"), "r", encoding="utf-8") as f:
    artifact_get = json.load(f)

assert status["event"]["state"] == "halted"
assert status["verification"]["ok"] is True
assert status["readiness"]["guestReady"]["ready"] is True
assert status["readiness"]["resultReady"]["ready"] is True
assert status["mediation"]["required"] is True
assert status["artifacts"]["egress"][0]["name"] == "report"
assert result["result"]["identity"]["runtimeID"] == workspace
assert result["result"]["exitCode"] == 0
assert artifacts["artifacts"]["ingress"][0]["name"] == "config"
assert artifacts["artifacts"]["egress"][0]["name"] == "report"
assert artifact_get["artifact"] == "report"
assert artifact_get["disk"] == "workspace"
with open(os.path.join(state_dir, "out", "report.json"), "r", encoding="utf-8") as f:
    body = json.load(f)
assert body["artifact"] == "report"
PY

echo "runtime contract smoke passed"
