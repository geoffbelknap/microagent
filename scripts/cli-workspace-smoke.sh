#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-workspace-smoke.XXXXXX")"
CLI="$STATE_DIR/microagent"
GUEST_INIT="$STATE_DIR/microagent-guestinit"
HELPER="$STATE_DIR/helper"
KERNEL="$STATE_DIR/Image"
RESULT="$STATE_DIR/result.json"

export GOCACHE="$STATE_DIR/gocache"
export GOMODCACHE="$STATE_DIR/gomodcache"
export GOFLAGS="${GOFLAGS:-} -modcacherw"

case "$(uname -m)" in
  arm64|aarch64)
    ARCH="${MICROAGENT_WORKSPACE_SMOKE_ARCH:-arm64}"
    IMAGE_DEFAULT="docker.io/library/busybox@sha256:bd44eb136a95dcc8dc58995e43abc40a413f2e8e3d4a2aae6bccbe94686acb05"
    ;;
  x86_64|amd64)
    ARCH="${MICROAGENT_WORKSPACE_SMOKE_ARCH:-amd64}"
    IMAGE_DEFAULT="docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f"
    ;;
  *)
    ARCH="${MICROAGENT_WORKSPACE_SMOKE_ARCH:-$(uname -m)}"
    IMAGE_DEFAULT="docker.io/library/busybox:1.36.1"
    ;;
esac
IMAGE="${MICROAGENT_WORKSPACE_SMOKE_IMAGE:-$IMAGE_DEFAULT}"

cleanup() {
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  rm -rf "$STATE_DIR"
}
trap cleanup EXIT

cat >"$HELPER" <<'PY'
#!/usr/bin/env python3
import datetime
import json
import sys

req = json.load(sys.stdin)
identity = req["identity"]
command = req["command"]
if command == "start":
    state = "starting"
elif command == "inspect":
    state = "stopped"
else:
    state = "prepared"
print(json.dumps({
    "ok": True,
    "backend": identity["backend"],
    "event": {
        "identity": identity,
        "state": state,
        "observedAt": datetime.datetime.now(datetime.UTC).isoformat().replace("+00:00", "Z"),
    },
}))
PY
chmod +x "$HELPER"
touch "$KERNEL"

(
  cd "$ROOT"
  go build -o "$CLI" ./cmd/microagent
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

"$CLI" run \
  --backend apple-vf \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --exec "echo workspace-smoke" \
  --name workspace-smoke \
  --kernel "$KERNEL" \
  --state-dir "$STATE_DIR" \
  --size-mib 64 \
  --result-port 0 \
  --guest-init "$GUEST_INIT" \
  --helper "$HELPER" >"$RESULT"

python3 - "$RESULT" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    result = json.load(f)

assert result["workspace"] == "workspace-smoke"
assert result["response"]["event"]["state"] == "stopped"
assert result["final_state"] == "stopped"
assert result["image"]["resolved_ref"]
assert result["image"]["output_path"].endswith("/workspaces/workspace-smoke/rootfs.ext4")
PY

echo "workspace CLI smoke passed"
