#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-text-output.XXXXXX")"
CLI="$STATE_DIR/microagent"
WORKSPACE="text-output"
KEEP_VAR="${MICROAGENT_KEEP_MICROAGENT_E2E_TEXT_OUTPUT:-0}"

case "$(uname -s)" in
  Darwin)
    HOST_BACKEND="apple-vf"
    ;;
  Linux)
    HOST_BACKEND="linux-kvm"
    ;;
  *)
    e2e_skip "unsupported host OS for microagent text output E2E: $(uname -s)"
    ;;
esac
case "$(uname -m)" in
  arm64|aarch64)
    GUEST_ARCH="arm64"
    ;;
  x86_64|amd64)
    GUEST_ARCH="amd64"
    ;;
  *)
    GUEST_ARCH="$(uname -m)"
    ;;
esac

cleanup() {
  status="$?"
  if [ -n "${STANDIN_PID:-}" ]; then
    kill "$STANDIN_PID" 2>/dev/null || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "$KEEP_VAR" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E text output state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

for required in go grep ps; do
  if ! command -v "$required" >/dev/null 2>&1; then
    e2e_skip "$required is required for microagent text output E2E"
  fi
done

export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
export GOFLAGS="${GOFLAGS:-} -modcacherw"

(cd "$ROOT" && go build -buildvcs=false -o "$CLI" ./cmd/microagent)

assert_stdout_contains() {
  name="$1"
  expected="$2"
  shift 2
  "$@" >"$STATE_DIR/${name}.out" 2>"$STATE_DIR/${name}.err"
  if ! grep -Eiq -- "$expected" "$STATE_DIR/${name}.out"; then
    echo "$name did not print expected output: $expected" >&2
    echo "--- stdout ---" >&2
    cat "$STATE_DIR/${name}.out" >&2
    echo "--- stderr ---" >&2
    cat "$STATE_DIR/${name}.err" >&2
    exit 1
  fi
}

assert_stdout_not_contains() {
  name="$1"
  unexpected="$2"
  if grep -Eiq -- "$unexpected" "$STATE_DIR/${name}.out"; then
    echo "$name printed unexpected output: $unexpected" >&2
    cat "$STATE_DIR/${name}.out" >&2
    exit 1
  fi
}

now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
kernel_path="$STATE_DIR/kernel.bin"
rootfs_path="$STATE_DIR/rootfs.ext4"
printf 'kernel' >"$kernel_path"
printf 'rootfs' >"$rootfs_path"
mkdir -p "$STATE_DIR/$WORKSPACE" "$STATE_DIR/workspaces/$WORKSPACE" "$STATE_DIR/images"

json_state_dir="$(e2e_host_path "$STATE_DIR")"
json_kernel_path="$(e2e_host_path "$kernel_path")"
json_rootfs_path="$(e2e_host_path "$rootfs_path")"

cat >"$STATE_DIR/workspaces/$WORKSPACE/workspace.json" <<JSON
{
  "name": "$WORKSPACE",
  "profile": "tiny",
  "restart": "never",
  "resources": {
    "memory_mib": 512,
    "cpu_count": 2,
    "size_mib": 96
  },
  "network": {
    "mode": "user",
    "port_forwards": [
      {
        "protocol": "tcp",
        "host": "127.0.0.1",
        "hostPort": 18080,
        "guestPort": 8080
      }
    ],
    "dns": ["1.1.1.1"],
    "routes": ["default"]
  },
  "artifacts": {
    "ingress": [
      {
        "name": "data",
        "path": "$json_state_dir/data.ext4",
        "mountpoint": "/data",
        "mode": "rw"
      }
    ],
    "egress": [
      {
        "name": "report",
        "path": "/tmp/report.txt"
      }
    ]
  }
}
JSON

cat >"$STATE_DIR/$WORKSPACE/event.json" <<JSON
{
  "identity": {
    "requestID": "text-output-request",
    "runtimeID": "$WORKSPACE",
    "role": "workload",
    "backend": "$HOST_BACKEND"
  },
  "state": "running",
  "detail": "seeded text output state",
  "observedAt": "$now"
}
JSON

# Stand-in for the workspace's firecracker: a live process whose argv carries the
# workspace state path so the status reconcile (pid liveness + argv ownership)
# treats the VM as alive and reports it running, instead of reaping the seeded
# state. Only Linux's supervisor reconciles this way; elsewhere e2e_host_pid is fine.
if [ "$HOST_BACKEND" = "linux-kvm" ]; then
  ( exec -a "$STATE_DIR/$WORKSPACE" sleep 600 ) &
  STANDIN_PID=$!
  WORKSPACE_PID=$STANDIN_PID
else
  WORKSPACE_PID="$(e2e_host_pid)"
fi

cat >"$STATE_DIR/$WORKSPACE/runtime.json" <<JSON
{
  "event": {
    "identity": {
      "requestID": "text-output-request",
      "runtimeID": "$WORKSPACE",
      "role": "workload",
      "backend": "$HOST_BACKEND"
    },
    "state": "running",
    "detail": "seeded text output state",
    "observedAt": "$now"
  },
  "config": {
    "kernelPath": "$json_kernel_path",
    "rootfsPath": "$json_rootfs_path",
    "stateDir": "$json_state_dir",
    "memoryMiB": 512,
    "cpuCount": 2,
    "network": {
      "mode": "user",
      "portForwards": [
        {
          "protocol": "tcp",
          "host": "127.0.0.1",
          "hostPort": 18080,
          "guestPort": 8080
        }
      ],
      "dns": ["1.1.1.1"],
      "routes": ["default"]
    }
  },
  "pid": $WORKSPACE_PID,
  "serialLogPath": "$json_state_dir/$WORKSPACE/serial.log",
  "startedAt": "$now",
  "updatedAt": "$now",
  "readiness": {
    "guestReady": {"ready": true, "observedAt": "$now"},
    "shellReady": {"ready": true, "observedAt": "$now"},
    "resultReady": {"ready": true, "observedAt": "$now"},
    "mediationReady": {"ready": false}
  }
}
JSON

cat >"$STATE_DIR/$WORKSPACE/result.json" <<JSON
{
  "started_at": "$now",
  "exited_at": "$now",
  "exit_code": 0,
  "stdout": "TEXT_STDOUT_OK\\n",
  "stderr": "TEXT_STDERR_OK\\n"
}
JSON

cat >"$STATE_DIR/images/index.json" <<JSON
{
  "images": [
    {
      "image_ref": "docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f",
      "resolved_ref": "docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f",
      "digest": "sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f",
      "platform": {
        "os": "linux",
        "architecture": "$GUEST_ARCH"
      },
      "output_path": "$json_rootfs_path",
      "size_bytes": 6,
      "last_used_at": "$now"
    },
    {
      "image_ref": "local/remove-alias:test",
      "resolved_ref": "docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f",
      "digest": "sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f",
      "platform": {
        "os": "linux",
        "architecture": "$GUEST_ARCH"
      },
      "output_path": "$json_rootfs_path",
      "size_bytes": 6,
      "last_used_at": "$now"
    },
    {
      "image_ref": "local/rmi-alias:test",
      "resolved_ref": "docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f",
      "digest": "sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f",
      "platform": {
        "os": "linux",
        "architecture": "$GUEST_ARCH"
      },
      "output_path": "$json_rootfs_path",
      "size_bytes": 6,
      "last_used_at": "$now"
    }
  ]
}
JSON

assert_stdout_contains contract-text "Contract:" "$CLI" --output text contract
assert_stdout_contains host-text "Host: $HOST_BACKEND on" "$CLI" --output text host --backend "$HOST_BACKEND" --arch "$GUEST_ARCH"
# --output accepts json|text only; human removed — see MIGRATION.md
assert_stdout_contains create-dry-run-text "Workspace: text-dry-run" \
  "$CLI" --output=text create text-dry-run --dry-run --image docker.io/library/busybox:1.36.1 --state-dir "$STATE_DIR" --network isolated
assert_stdout_contains status-text "Readiness: guest=ready shell=not-ready result=ready mediation=disabled" \
  "$CLI" --output text status "$WORKSPACE" --state-dir "$STATE_DIR"
assert_stdout_contains result-text "TEXT_STDOUT_OK" \
  "$CLI" --output text result "$WORKSPACE" --state-dir "$STATE_DIR"
assert_stdout_contains network-text "Forward: tcp 127.0.0.1:18080 -> guest:8080" \
  "$CLI" --output text network "$WORKSPACE" --state-dir "$STATE_DIR"
assert_stdout_contains artifact-text "Egress: 1" \
  "$CLI" --output text artifact "$WORKSPACE" --state-dir "$STATE_DIR"
assert_stdout_contains list-text "NAME[[:space:]]+STATE[[:space:]]+BACKEND" \
  "$CLI" --output text list --state-dir "$STATE_DIR"
assert_stdout_contains images-list-text "docker.io/library/busybox" \
  "$CLI" --output text image list --state-dir "$STATE_DIR"
assert_stdout_not_contains images-list-text '"images"'
assert_stdout_contains images-delete-text '"removed"' \
  "$CLI" image delete local/remove-alias:test --state-dir "$STATE_DIR"
assert_stdout_contains perf-footprint-text "^Footprint benchmark —" \
  "$CLI" --output text perf footprint "$WORKSPACE" --state-dir "$STATE_DIR"
assert_stdout_contains perf-steady-text "^Steady memory$" \
  "$CLI" --output text perf steady "$WORKSPACE" --duration 1 --interval 1 --state-dir "$STATE_DIR"

MICROAGENT_OUTPUT=text assert_stdout_contains env-text-output "No workspaces." \
  "$CLI" list --state-dir "$STATE_DIR/empty"

echo "microagent E2E text output passed"
