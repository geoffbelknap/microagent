#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-text-output.XXXXXX")"
CLI="$STATE_DIR/microagent"
WORKSPACE="text-output"
KEEP_VAR="${MICROAGENT_KEEP_MICROAGENT_E2E_TEXT_OUTPUT:-0}"

cleanup() {
  status="$?"
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
    echo "$required is required for microagent text output E2E" >&2
    exit 2
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
        "path": "$STATE_DIR/data.ext4",
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
    "backend": "firecracker"
  },
  "state": "running",
  "detail": "seeded text output state",
  "observedAt": "$now"
}
JSON

cat >"$STATE_DIR/$WORKSPACE/runtime.json" <<JSON
{
  "event": {
    "identity": {
      "requestID": "text-output-request",
      "runtimeID": "$WORKSPACE",
      "role": "workload",
      "backend": "firecracker"
    },
    "state": "running",
    "detail": "seeded text output state",
    "observedAt": "$now"
  },
  "config": {
    "kernelPath": "$kernel_path",
    "rootfsPath": "$rootfs_path",
    "stateDir": "$STATE_DIR",
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
  "pid": $$,
  "serialLogPath": "$STATE_DIR/$WORKSPACE/serial.log",
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
        "architecture": "amd64"
      },
      "output_path": "$rootfs_path",
      "size_bytes": 6,
      "last_used_at": "$now"
    },
    {
      "image_ref": "local/remove-alias:test",
      "resolved_ref": "docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f",
      "digest": "sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f",
      "platform": {
        "os": "linux",
        "architecture": "amd64"
      },
      "output_path": "$rootfs_path",
      "size_bytes": 6,
      "last_used_at": "$now"
    },
    {
      "image_ref": "local/rmi-alias:test",
      "resolved_ref": "docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f",
      "digest": "sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f",
      "platform": {
        "os": "linux",
        "architecture": "amd64"
      },
      "output_path": "$rootfs_path",
      "size_bytes": 6,
      "last_used_at": "$now"
    }
  ]
}
JSON

assert_stdout_contains contract-text "Contract:" "$CLI" --text contract
assert_stdout_contains host-text "Backend:" "$CLI" --output text host --backend firecracker --arch amd64
assert_stdout_contains host-human "Backend:" "$CLI" --human host --backend firecracker --arch amd64
assert_stdout_contains create-dry-run-text "Workspace: text-dry-run" \
  "$CLI" --output=text create text-dry-run --dry-run --image docker.io/library/busybox:1.36.1 --state-dir "$STATE_DIR" --network isolated
assert_stdout_contains status-text "Readiness: guest=ready shell=not-ready result=ready mediation=disabled" \
  "$CLI" --text status "$WORKSPACE" --state-dir "$STATE_DIR"
assert_stdout_contains result-text "TEXT_STDOUT_OK" \
  "$CLI" --output text result "$WORKSPACE" --state-dir "$STATE_DIR"
assert_stdout_contains network-text "Forward: tcp 127.0.0.1:18080 -> guest:8080" \
  "$CLI" --text network "$WORKSPACE" --state-dir "$STATE_DIR"
assert_stdout_contains artifacts-text "Egress: 1" \
  "$CLI" --text artifacts "$WORKSPACE" --state-dir "$STATE_DIR"
assert_stdout_contains ps-text "NAME[[:space:]]+STATE[[:space:]]+BACKEND" \
  "$CLI" --text ps --state-dir "$STATE_DIR"
assert_stdout_contains images-list-text "docker.io/library/busybox" \
  "$CLI" --text images list --state-dir "$STATE_DIR"
assert_stdout_not_contains images-list-text '"images"'
assert_stdout_contains images-remove-alias '"removed"' \
  "$CLI" images remove local/remove-alias:test --state-dir "$STATE_DIR"
assert_stdout_contains images-rmi-alias '"removed"' \
  "$CLI" images rmi local/rmi-alias:test --state-dir "$STATE_DIR"
assert_stdout_contains perf-footprint-text "Benchmark: footprint" \
  "$CLI" --output text perf footprint "$WORKSPACE" --state-dir "$STATE_DIR"
assert_stdout_contains perf-steady-text "Samples:" \
  "$CLI" --text perf steady "$WORKSPACE" --duration 1 --interval 1 --state-dir "$STATE_DIR"

MICROAGENT_OUTPUT=text assert_stdout_contains env-text-output "No workspaces." \
  "$CLI" ps --state-dir "$STATE_DIR/empty"
assert_stdout_contains output-human "No workspaces." \
  "$CLI" --output=human ps --state-dir "$STATE_DIR/empty-human"

echo "microagent E2E text output passed"
