#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-workspace-smoke.XXXXXX")"
CLI="$STATE_DIR/microagent"
HELPER="$STATE_DIR/helper"
KERNEL="$STATE_DIR/Image"
RESULT="$STATE_DIR/result.json"

export GOCACHE="$STATE_DIR/gocache"
export GOMODCACHE="$STATE_DIR/gomodcache"
export GOFLAGS="${GOFLAGS:-} -modcacherw"

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
)

"$CLI" run \
  --image docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6 \
  --exec "echo workspace-smoke" \
  --name workspace-smoke \
  --kernel "$KERNEL" \
  --state-dir "$STATE_DIR" \
  --size-mib 64 \
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
