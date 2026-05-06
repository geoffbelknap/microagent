#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-firecracker-smoke.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
RESULT="$STATE_DIR/result.json"
EXPECTED_OUTPUT="microagent-firecracker-boot-smoke"
IMAGE="docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f"
EXPECTED_KERNEL_SHA="4bbe8b2fd19f78fea4bf02d52a67482227a896c90a63f272b6a084fa46a416c0"

cleanup() {
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  rm -rf "$STATE_DIR"
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    echo "firecracker boot smoke requires Linux amd64" >&2
    exit 2
    ;;
esac

if [ ! -e /dev/kvm ]; then
  echo "/dev/kvm is not visible; run this smoke outside the Codex sandbox" >&2
  exit 2
fi

if [ -n "${MICROAGENT_FIRECRACKER:-}" ]; then
  firecracker="$MICROAGENT_FIRECRACKER"
elif command -v firecracker >/dev/null 2>&1; then
  firecracker="$(command -v firecracker)"
elif command -v brew >/dev/null 2>&1; then
  formula_prefix="$(brew --prefix microagent-kit 2>/dev/null || true)"
  firecracker="$formula_prefix/libexec/firecracker"
else
  firecracker=""
fi

if [ ! -x "${firecracker:-}" ]; then
  echo "firecracker binary not found; install microagent-kit or set MICROAGENT_FIRECRACKER inside the script environment" >&2
  exit 2
fi

export GOCACHE="$STATE_DIR/gocache"
export GOMODCACHE="$STATE_DIR/gomodcache"
export GOFLAGS="${GOFLAGS:-} -modcacherw"
export MICROAGENT_FIRECRACKER="$firecracker"
export MICROAGENT_FIRECRACKER_SUPERVISOR="$SUPERVISOR"

(
  cd "$ROOT"
  go build -o "$CLI" ./cmd/microagent
  go build -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

"$CLI" kernel install --backend firecracker --arch amd64 >"$STATE_DIR/kernel-install.json"

kernel_path="$(python3 - "$STATE_DIR/kernel-install.json" "$EXPECTED_KERNEL_SHA" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)

if result.get("sha256") != sys.argv[2]:
    raise SystemExit(f"kernel sha256 = {result.get('sha256')}, want {sys.argv[2]}")

print(result["path"])
PY
)"

"$CLI" run \
  --backend firecracker \
  --image "$IMAGE" \
  --arch amd64 \
  --exec "echo $EXPECTED_OUTPUT" \
  --kernel "$kernel_path" \
  --guest-init "$GUEST_INIT" \
  --state-dir "$STATE_DIR/run" \
  --size-mib 128 \
  --result-port 0 \
  --timeout 30 \
  --keep >"$RESULT"

python3 - "$RESULT" "$EXPECTED_OUTPUT" "$EXPECTED_KERNEL_SHA" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)

expected_output = sys.argv[2]
expected_kernel_sha = sys.argv[3]

assert result["response"]["ok"] is True
assert result["response"]["backend"] == "firecracker"
assert result["final_state"] == "stopped"
assert result["image"]["platform"]["architecture"] == "amd64"
assert expected_output in result["serial_log"]
assert "reboot: System halted" in result["serial_log"] or "reboot: Power down" in result["serial_log"]
assert expected_kernel_sha
PY

echo "firecracker boot smoke passed"
echo "kernel_sha=$EXPECTED_KERNEL_SHA"
