#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-public-surface.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit"
WORKSPACE="public-surface"
BUNDLE_WORKSPACE="public-bundle"
DISK_WORKSPACE="public-disk"
PERF_WORKSPACE="public-perf"
RUN_KEEP_WORKSPACE="public-run-keep"
JSON_WORKSPACE="public-json"
JSON_STDIN_WORKSPACE="public-json-stdin"
OPTIONS_RUN_WORKSPACE="public-options-run"
SERVICE_WORKSPACE="public-service-options"
IMAGE_COMMAND_WORKSPACE="public-image-command"
ROOTFS="$STATE_DIR/rootfs.ext4"
ARTIFACT_DIR="$STATE_DIR/artifacts"
KEEP_VAR="${MICROAGENT_KEEP_MICROAGENT_E2E_PUBLIC_SURFACE:-0}"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    for workspace in "$WORKSPACE" "$BUNDLE_WORKSPACE" "$DISK_WORKSPACE" "$PERF_WORKSPACE" "$RUN_KEEP_WORKSPACE" "$JSON_WORKSPACE" "$JSON_STDIN_WORKSPACE" "$OPTIONS_RUN_WORKSPACE" "$SERVICE_WORKSPACE" "$IMAGE_COMMAND_WORKSPACE" public-docker-run public-docker-image-command implicit-spec high-dry-run missing-result missing-artifact corrupt-state invalid-name; do
      "$CLI" stop "$workspace" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      "$CLI" kill "$workspace" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      if [ "$status" -eq 0 ]; then
        "$CLI" delete "$workspace" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      fi
    done
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "$KEEP_VAR" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent public surface E2E state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -m)" in
  x86_64|amd64)
    ARCH="amd64"
    IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox@sha256:b7f3d86d6e84fc17718c48bcde1450807faa2d56704205c697b4bd5df7b9e29f}"
    ;;
  arm64|aarch64)
    ARCH="arm64"
    IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox@sha256:bd44eb136a95dcc8dc58995e43abc40a413f2e8e3d4a2aae6bccbe94686acb05}"
    ;;
  *)
    ARCH="$(uname -m)"
    IMAGE="${MICROAGENT_E2E_IMAGE:-docker.io/library/busybox:1.36.1}"
    ;;
esac
export MICROAGENT_ROOTFS_BASE_CACHE_DIR="${MICROAGENT_ROOTFS_BASE_CACHE_DIR:-$ROOT/.cache/microagent-e2e/rootfs-base-cache/busybox-$ARCH}"
if [ "${MICROAGENT_E2E_REFRESH_IMAGE_CACHE:-0}" = "1" ]; then
  export MICROAGENT_ROOTFS_BASE_CACHE_REFRESH=1
fi

for required in go python3 debugfs mke2fs tar; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "$required is required for microagent public surface E2E" >&2
    exit 2
  fi
done

if [ "$(uname -s)" = "Linux" ]; then
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
    echo "microagent public surface E2E requires the host backend binary on Linux; install firecracker on PATH or set MICROAGENT_FIRECRACKER" >&2
    exit 2
  fi
  export MICROAGENT_FIRECRACKER="$firecracker"
  export MICROAGENT_FIRECRACKER_SUPERVISOR="$SUPERVISOR"
fi

export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
export GOFLAGS="${GOFLAGS:-} -modcacherw"

json_get() {
  python3 - "$1" "$2" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    value = json.load(f)
for part in sys.argv[2].split("."):
    if not part:
        continue
    if isinstance(value, list):
        value = value[int(part)]
    else:
        value = value[part]
print(value)
PY
}

assert_json() {
  python3 - "$1" "$2" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    data = json.load(f)
expr = sys.argv[2]
helpers = {"any": any, "all": all, "len": len}
if not eval(expr, {"__builtins__": {}}, {"data": data, **helpers}):
    raise SystemExit(f"assertion failed: {expr}\n{json.dumps(data, indent=2)}")
PY
}

expect_failure() {
  name="$1"
  expected="$2"
  shift 2
  if "$@" >"$STATE_DIR/${name}.out" 2>"$STATE_DIR/${name}.err"; then
    echo "$name unexpectedly succeeded" >&2
    exit 1
  fi
  if ! grep -qi "$expected" "$STATE_DIR/${name}.err"; then
    echo "$name failed without expected message: $expected" >&2
    cat "$STATE_DIR/${name}.err" >&2
    exit 1
  fi
}

expect_tty_cancel() {
  name="$1"
  expected="$2"
  shift 2
  if command -v script >/dev/null 2>&1 && script --version 2>/dev/null | grep -qi "util-linux"; then
    printf -v command '%q ' "$@"
    if printf 'n\n' | script -qfec "$command" /dev/null >"$STATE_DIR/${name}.out" 2>"$STATE_DIR/${name}.err"; then
      echo "$name unexpectedly succeeded" >&2
      exit 1
    fi
    if ! grep -qia "$expected" "$STATE_DIR/${name}.out" && ! grep -qia "$expected" "$STATE_DIR/${name}.err"; then
      echo "$name failed without expected message: $expected" >&2
      cat "$STATE_DIR/${name}.out" >&2
      cat "$STATE_DIR/${name}.err" >&2
      exit 1
    fi
    return 0
  fi
  expect_failure "$name" "pass --yes" "$@"
}

wait_for_status_ready() {
  workspace="$1"
  output="$2"
  deadline="$((SECONDS + 60))"
  while true; do
    "$CLI" --json status "$workspace" --state-dir "$STATE_DIR" >"$output"
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

wait_for_result() {
  workspace="$1"
  output="$2"
  deadline="$((SECONDS + 60))"
  while true; do
    if "$CLI" --json result "$workspace" --state-dir "$STATE_DIR" >"$output" 2>"$output.err"; then
      return 0
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "workspace $workspace did not publish a result" >&2
      cat "$output.err" >&2 || true
      return 1
    fi
    sleep 1
  done
}

(
  cd "$ROOT"
  go build -buildvcs=false -o "$CLI" ./cmd/microagent
  if [ "$(uname -s)" = "Linux" ]; then
    go build -buildvcs=false -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
  fi
  GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit
)

"$CLI" version >"$STATE_DIR/version.txt"
grep -q '^microagent ' "$STATE_DIR/version.txt"
"$CLI" --json contract >"$STATE_DIR/contract.json"
assert_json "$STATE_DIR/contract.json" "any(item.get('name') == 'running' for item in data.get('states', []))"
assert_json "$STATE_DIR/contract.json" "any(item.get('name') == 'exitCode' for item in data.get('resultFields', []))"
"$CLI" --json profiles >"$STATE_DIR/profiles.json"
assert_json "$STATE_DIR/profiles.json" "any(profile.get('name') == 'small' for profile in data.get('profiles', []))"
"$CLI" --json host >"$STATE_DIR/host.json"
"$CLI" --json doctor >"$STATE_DIR/doctor.json"
assert_json "$STATE_DIR/doctor.json" "data.get('ok') is True"
backend="$(json_get "$STATE_DIR/doctor.json" "backend")"

"$CLI" kernel install --backend "$backend" --arch "$ARCH" >"$STATE_DIR/kernel-install.json"
kernel_path="$(json_get "$STATE_DIR/kernel-install.json" "path")"
kernel_sha="$(json_get "$STATE_DIR/kernel-install.json" "sha256")"
"$CLI" kernel verify --backend "$backend" --arch "$ARCH" --path "$kernel_path" --sha256 "$kernel_sha" >"$STATE_DIR/kernel-verify.json"
assert_json "$STATE_DIR/kernel-verify.json" "data.get('ok') is True"
expect_failure kernel-verify-mismatch "want" \
  "$CLI" kernel verify --backend "$backend" --arch "$ARCH" --path "$kernel_path" --sha256 0000000000000000000000000000000000000000000000000000000000000000
cp "$kernel_path" "$STATE_DIR/local-kernel-source"
"$CLI" kernel install \
  --backend "$backend" \
  --arch "$ARCH" \
  --from "$STATE_DIR/local-kernel-source" \
  --sha256 "$kernel_sha" \
  --out "$STATE_DIR/imported-kernels/$backend/$ARCH/Image" >"$STATE_DIR/kernel-install-from.json"
assert_json "$STATE_DIR/kernel-install-from.json" "data.get('path') == '$STATE_DIR/imported-kernels/$backend/$ARCH/Image'"
assert_json "$STATE_DIR/kernel-install-from.json" "data.get('sha256') == '$kernel_sha'"
"$CLI" kernel verify \
  --backend "$backend" \
  --arch "$ARCH" \
  --path "$STATE_DIR/imported-kernels/$backend/$ARCH/Image" \
  --sha256 "$kernel_sha" >"$STATE_DIR/kernel-verify-from.json"
assert_json "$STATE_DIR/kernel-verify-from.json" "data.get('ok') is True"

"$CLI" rootfs build \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --init "$GUEST_INIT" \
  --out "$ROOTFS" \
  --state-dir "$STATE_DIR/rootfs-build" \
  --size-mib 96 >"$STATE_DIR/rootfs-build.json"
assert_json "$STATE_DIR/rootfs-build.json" "data.get('outputPath') or data.get('output_path')"
"$CLI" rootfs build \
  --image docker.io/library/busybox:1.36.1 \
  --allow-mutable \
  --keep-stage \
  --stage-snapshot "$STATE_DIR/rootfs-stage-snapshot" \
  --arch "$ARCH" \
  --init "$GUEST_INIT" \
  --out "$STATE_DIR/rootfs-advanced.ext4" \
  --state-dir "$STATE_DIR/rootfs-advanced-build" \
  --size-mib 96 >"$STATE_DIR/rootfs-advanced-build.json"
assert_json "$STATE_DIR/rootfs-advanced-build.json" "data.get('image_ref') == 'docker.io/library/busybox:1.36.1'"
assert_json "$STATE_DIR/rootfs-advanced-build.json" "data.get('resolved_ref', '').startswith('docker.io/library/busybox@sha256:')"
assert_json "$STATE_DIR/rootfs-advanced-build.json" "data.get('stage_dir', '') != ''"
assert_json "$STATE_DIR/rootfs-advanced-build.json" "data.get('stage_snapshot') == '$STATE_DIR/rootfs-stage-snapshot'"
test -e "$STATE_DIR/rootfs-advanced.ext4"
advanced_stage="$(json_get "$STATE_DIR/rootfs-advanced-build.json" "stage_dir")"
advanced_init="$(json_get "$STATE_DIR/rootfs-advanced-build.json" "init_path")"
advanced_init="${advanced_init#/}"
test -e "$advanced_stage/$advanced_init"
test -e "$STATE_DIR/rootfs-stage-snapshot/$advanced_init"
expect_failure rootfs-missing-image "image_ref is required" \
  "$CLI" rootfs build --arch "$ARCH" --init "$GUEST_INIT" --out "$STATE_DIR/rootfs-missing-image.ext4" --state-dir "$STATE_DIR/rootfs-missing-image"
expect_failure rootfs-missing-output "output_path is required" \
  "$CLI" rootfs build --image "$IMAGE" --arch "$ARCH" --init "$GUEST_INIT" --state-dir "$STATE_DIR/rootfs-missing-output"
expect_failure rootfs-negative-size "size_mib must not be negative" \
  "$CLI" rootfs build --image "$IMAGE" --arch "$ARCH" --init "$GUEST_INIT" --out "$STATE_DIR/rootfs-negative-size.ext4" --state-dir "$STATE_DIR/rootfs-negative-size" --size-mib -1
expect_failure rootfs-mutable-ref "immutable" \
  "$CLI" rootfs build --image docker.io/library/busybox:latest --arch "$ARCH" --init "$GUEST_INIT" --out "$STATE_DIR/rootfs-mutable.ext4" --state-dir "$STATE_DIR/rootfs-mutable" --size-mib 96
expect_failure rootfs-invalid-ref "parse OCI image ref" \
  "$CLI" rootfs build --image "bad ref@sha256:abc" --arch "$ARCH" --init "$GUEST_INIT" --out "$STATE_DIR/rootfs-invalid-ref.ext4" --state-dir "$STATE_DIR/rootfs-invalid-ref" --size-mib 96
expect_failure rootfs-unsupported-platform "fetch OCI image\\|platform\\|manifest\\|architecture" \
  "$CLI" rootfs build --image "$IMAGE" --arch definitely-unsupported --init "$GUEST_INIT" --out "$STATE_DIR/rootfs-unsupported-platform.ext4" --state-dir "$STATE_DIR/rootfs-unsupported-platform" --size-mib 96

ROOTFS_REFRESH_CACHE="$STATE_DIR/rootfs-refresh-cache"
env MICROAGENT_ROOTFS_BASE_CACHE_DIR="$ROOTFS_REFRESH_CACHE" \
  "$CLI" rootfs build \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --init "$GUEST_INIT" \
  --out "$STATE_DIR/rootfs-refresh-seed.ext4" \
  --state-dir "$STATE_DIR/rootfs-refresh-seed" \
  --size-mib 96 >"$STATE_DIR/rootfs-refresh-seed.json"
test -e "$STATE_DIR/rootfs-refresh-seed.ext4"
find "$ROOTFS_REFRESH_CACHE" -name metadata.json -print -quit >"$STATE_DIR/rootfs-refresh-metadata-path.txt"
ROOTFS_REFRESH_METADATA="$(cat "$STATE_DIR/rootfs-refresh-metadata-path.txt")"
test -n "$ROOTFS_REFRESH_METADATA"
printf '{not-json\n' >"$ROOTFS_REFRESH_METADATA"
expect_failure rootfs-corrupt-base-cache "parse rootfs base cache metadata\\|invalid" \
  env MICROAGENT_ROOTFS_BASE_CACHE_DIR="$ROOTFS_REFRESH_CACHE" \
  "$CLI" rootfs build \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --init "$GUEST_INIT" \
  --out "$STATE_DIR/rootfs-corrupt-base-cache.ext4" \
  --state-dir "$STATE_DIR/rootfs-corrupt-base-cache" \
  --size-mib 96
env MICROAGENT_ROOTFS_BASE_CACHE_DIR="$ROOTFS_REFRESH_CACHE" MICROAGENT_ROOTFS_BASE_CACHE_REFRESH=1 \
  "$CLI" rootfs build \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --init "$GUEST_INIT" \
  --out "$STATE_DIR/rootfs-refresh-recovered.ext4" \
  --state-dir "$STATE_DIR/rootfs-refresh-recovered" \
  --size-mib 96 >"$STATE_DIR/rootfs-refresh-recovered.json"
test -e "$STATE_DIR/rootfs-refresh-recovered.ext4"
expect_failure rootfs-bad-mke2fs "build ext4 rootfs\\|mke2fs\\|no such file" \
  env MICROAGENT_ROOTFS_BASE_CACHE_DIR="$ROOTFS_REFRESH_CACHE" \
  "$CLI" rootfs build \
  --image "$IMAGE" \
  --arch "$ARCH" \
  --init "$GUEST_INIT" \
  --out "$STATE_DIR/rootfs-bad-mke2fs.ext4" \
  --state-dir "$STATE_DIR/rootfs-bad-mke2fs" \
  --mke2fs "$STATE_DIR/not-mke2fs" \
  --size-mib 96

expect_failure images-pull-missing-ref "usage: microagent images pull" \
  "$CLI" images pull --state-dir "$STATE_DIR"
expect_failure images-pull-invalid-ref "parse OCI image ref" \
  "$CLI" images pull "bad ref@sha256:abc" --state-dir "$STATE_DIR" --arch "$ARCH" --guest-init "$GUEST_INIT" --size-mib 96
expect_failure images-rm-missing-ref "image reference is required" \
  "$CLI" images rm "" --state-dir "$STATE_DIR"
expect_failure images-tag-missing-source "source image is required" \
  "$CLI" images tag "" local/microagent:test --state-dir "$STATE_DIR"
expect_failure images-tag-missing-target "target image is required" \
  "$CLI" images tag "$IMAGE" "" --state-dir "$STATE_DIR"
mkdir -p "$STATE_DIR/missing-image-cache/images"
python3 - "$STATE_DIR/missing-image-cache/images/index.json" "$IMAGE" "$ARCH" <<'PY'
import json
import sys
import time

path, image, arch = sys.argv[1:4]
record = {
    "image_ref": image,
    "resolved_ref": image,
    "digest": image.rsplit("@", 1)[-1] if "@" in image else "",
    "platform": {"os": "linux", "architecture": arch},
    "output_path": "/tmp/microagent-missing-rootfs-cache-entry.ext4",
    "size_bytes": 1,
    "last_used_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
}
with open(path, "w", encoding="utf-8") as f:
    json.dump({"images": [record]}, f, indent=2, sort_keys=True)
    f.write("\n")
PY
"$CLI" images prune --state-dir "$STATE_DIR/missing-image-cache" >"$STATE_DIR/images-prune-missing-rootfs.json"
assert_json "$STATE_DIR/images-prune-missing-rootfs.json" "len(data.get('removed') or []) == 1 and len(data.get('kept') or []) == 0"
printf '{not-json\n' >"$STATE_DIR/missing-image-cache/images/index.json"
expect_failure images-corrupt-index "invalid\\|json" \
  "$CLI" images list --state-dir "$STATE_DIR/missing-image-cache"

mkdir -p "$STATE_DIR/workspaces/confirm-delete"
cat >"$STATE_DIR/workspaces/confirm-delete/workspace.json" <<'JSON'
{
  "name": "confirm-delete",
  "profile": "small",
  "restart": "never",
  "resources": {
    "memory_mib": 512,
    "cpu_count": 2,
    "size_mib": 96
  },
  "network": {
    "mode": "isolated"
  }
}
JSON
expect_tty_cancel delete-confirm-cancel "delete cancelled\\|pass --yes" \
  "$CLI" delete confirm-delete --state-dir "$STATE_DIR"
test -e "$STATE_DIR/workspaces/confirm-delete/workspace.json"
"$CLI" delete confirm-delete --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete-confirm-yes.json"
assert_json "$STATE_DIR/delete-confirm-yes.json" "data.get('event', {}).get('state') == 'stopped'"

python3 - "$STATE_DIR/confirm-images" <<'PY'
import json
import os
import sys
import time

state_dir = sys.argv[1]
rootfs_dir = os.path.join(state_dir, "images", "rootfs")
os.makedirs(rootfs_dir, exist_ok=True)
records = []
for name in ("rm-cancel", "rm-yes", "prune-cancel", "prune-yes"):
    path = os.path.join(rootfs_dir, f"{name}.ext4")
    with open(path, "wb") as f:
        f.write(name.encode("utf-8"))
    records.append({
        "image_ref": f"local/{name}:test",
        "resolved_ref": f"local/{name}:test",
        "digest": f"sha256:{name}",
        "platform": {"os": "linux", "architecture": "amd64"},
        "output_path": path,
        "size_bytes": os.path.getsize(path),
        "last_used_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    })
os.makedirs(os.path.join(state_dir, "images"), exist_ok=True)
with open(os.path.join(state_dir, "images", "index.json"), "w", encoding="utf-8") as f:
    json.dump({"images": records}, f, indent=2, sort_keys=True)
    f.write("\n")
PY
expect_tty_cancel images-rm-delete-cancel "prune cancelled\\|pass --yes" \
  "$CLI" images rm local/rm-cancel:test --delete --state-dir "$STATE_DIR/confirm-images"
test -e "$STATE_DIR/confirm-images/images/rootfs/rm-cancel.ext4"
"$CLI" images rm local/rm-yes:test --delete --yes --state-dir "$STATE_DIR/confirm-images" >"$STATE_DIR/images-rm-delete-yes.json"
test ! -e "$STATE_DIR/confirm-images/images/rootfs/rm-yes.ext4"
expect_tty_cancel images-prune-delete-cancel "prune cancelled\\|pass --yes" \
  "$CLI" images prune --delete --state-dir "$STATE_DIR/confirm-images"
test -e "$STATE_DIR/confirm-images/images/rootfs/prune-cancel.ext4"
"$CLI" images prune --delete --yes --state-dir "$STATE_DIR/confirm-images" >"$STATE_DIR/images-prune-delete-yes.json"
test ! -e "$STATE_DIR/confirm-images/images/rootfs/prune-yes.ext4"

python3 - "$STATE_DIR/confirm-prune-y" <<'PY'
import json
import os
import sys
import time

state_dir = sys.argv[1]
rootfs_dir = os.path.join(state_dir, "images", "rootfs")
os.makedirs(rootfs_dir, exist_ok=True)
records = []
for name in ("cancel", "yes"):
    path = os.path.join(rootfs_dir, f"{name}.ext4")
    with open(path, "wb") as f:
        f.write(name.encode("utf-8"))
    records.append({
        "image_ref": f"local/top-prune-{name}:test",
        "resolved_ref": f"local/top-prune-{name}:test",
        "digest": f"sha256:top-prune-{name}",
        "platform": {"os": "linux", "architecture": "amd64"},
        "output_path": path,
        "size_bytes": os.path.getsize(path),
        "last_used_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    })
os.makedirs(os.path.join(state_dir, "images"), exist_ok=True)
with open(os.path.join(state_dir, "images", "index.json"), "w", encoding="utf-8") as f:
    json.dump({"images": records}, f, indent=2, sort_keys=True)
    f.write("\n")
PY
expect_tty_cancel prune-images-cancel "prune cancelled\\|pass --yes" \
  "$CLI" prune --images --state-dir "$STATE_DIR/confirm-prune-y"
test -e "$STATE_DIR/confirm-prune-y/images/rootfs/cancel.ext4"
"$CLI" prune --images -y --state-dir "$STATE_DIR/confirm-prune-y" >"$STATE_DIR/prune-images-y.json"
test ! -e "$STATE_DIR/confirm-prune-y/images/rootfs/yes.ext4"

"$CLI" --json create high-dry-run \
  --dry-run \
  --image "$IMAGE" \
  --guest-init "$GUEST_INIT" \
  --kernel "$kernel_path" \
  --state-dir "$STATE_DIR" \
  --network isolated \
  --size-mib 96 >"$STATE_DIR/create-high-dry-run.json"
assert_json "$STATE_DIR/create-high-dry-run.json" "data.get('workspace') == 'high-dry-run'"
assert_json "$STATE_DIR/create-high-dry-run.json" "data.get('response', {}).get('ok') is True"
assert_json "$STATE_DIR/create-high-dry-run.json" "data.get('response', {}).get('event', {}).get('state') == 'prepared'"
test ! -e "$STATE_DIR/workspaces/high-dry-run"

mkdir -p "$STATE_DIR/implicit-spec"
cat >"$STATE_DIR/implicit-spec/microagent.yaml" <<YAML
name: implicit-spec
image: $IMAGE
profile: tiny
restart: never
resources:
  memoryMiB: 512
  cpuCount: 2
  sizeMiB: 96
network:
  mode: isolated
YAML
(
  cd "$STATE_DIR/implicit-spec"
  "$CLI" --json create \
    --dry-run \
    --state-dir "$STATE_DIR" \
    --kernel "$kernel_path" \
    --guest-init "$GUEST_INIT" >"$STATE_DIR/create-implicit-spec.json"
)
assert_json "$STATE_DIR/create-implicit-spec.json" "data.get('workspace') == 'implicit-spec'"
assert_json "$STATE_DIR/create-implicit-spec.json" "data.get('profile') == 'tiny'"

expect_failure invalid-create-name "invalid workspace name" \
  "$CLI" create "../invalid-name" --dry-run --image "$IMAGE" --state-dir "$STATE_DIR"
expect_failure invalid-status-name "invalid workspace name" \
  "$CLI" status "../invalid-name" --state-dir "$STATE_DIR"

expect_failure apply-missing-file "apply requires --file" \
  "$CLI" --json apply --state-dir "$STATE_DIR"
cat >"$STATE_DIR/apply-invalid-name.yaml" <<'YAML'
name: ../invalid-apply
restart: always
YAML
expect_failure apply-invalid-name "invalid workspace name" \
  "$CLI" --json apply --file "$STATE_DIR/apply-invalid-name.yaml" --state-dir "$STATE_DIR"
cat >"$STATE_DIR/apply-missing-workspace.yaml" <<'YAML'
name: apply-missing-workspace
restart: always
YAML
expect_failure apply-missing-workspace "workspace.json" \
  "$CLI" --json apply --file "$STATE_DIR/apply-missing-workspace.yaml" --state-dir "$STATE_DIR"

mkdir -p "$STATE_DIR/workspaces/apply-edge"
cat >"$STATE_DIR/workspaces/apply-edge/workspace.json" <<'JSON'
{
  "name": "apply-edge",
  "profile": "small",
  "restart": "never",
  "resources": {
    "memory_mib": 512,
    "cpu_count": 2,
    "size_mib": 96
  },
  "network": {
    "mode": "isolated"
  }
}
JSON
cp "$STATE_DIR/workspaces/apply-edge/workspace.json" "$STATE_DIR/apply-edge-before.json"
cat >"$STATE_DIR/apply-edge-noop.yaml" <<'YAML'
name: apply-edge
restart: never
YAML
"$CLI" --json apply --file "$STATE_DIR/apply-edge-noop.yaml" --state-dir "$STATE_DIR" >"$STATE_DIR/apply-edge-noop.json"
assert_json "$STATE_DIR/apply-edge-noop.json" "data.get('workspace') == 'apply-edge' and data.get('applied') is None"
cat >"$STATE_DIR/apply-edge-unsupported.yaml" <<'YAML'
name: apply-edge
resources:
  memoryMiB: 768
  cpuCount: 1
setup:
  - echo should-not-apply
service: sleep 300
files:
  - src: ./missing.txt
    dst: /missing.txt
YAML
"$CLI" --json apply --file "$STATE_DIR/apply-edge-unsupported.yaml" --state-dir "$STATE_DIR" >"$STATE_DIR/apply-edge-unsupported.json"
assert_json "$STATE_DIR/apply-edge-unsupported.json" "data.get('workspace') == 'apply-edge' and data.get('applied') is None"
cmp "$STATE_DIR/apply-edge-before.json" "$STATE_DIR/workspaces/apply-edge/workspace.json"

"$CLI" create \
  --dry-run \
  --id dry-run-check \
  --backend "$backend" \
  --kernel "$kernel_path" \
  --rootfs "$ROOTFS" \
  --state-dir "$STATE_DIR" \
  --network isolated >"$STATE_DIR/create-dry-run.json"
assert_json "$STATE_DIR/create-dry-run.json" "data.get('ok') is True and data.get('backend') == '$backend'"

truncate -s 16M "$STATE_DIR/json-extra-disk.ext4"
python3 - "$STATE_DIR/request.json" "$backend" "$kernel_path" "$ROOTFS" "$STATE_DIR" "$STATE_DIR/json-extra-disk.ext4" <<'PY'
import json
import sys

path, backend, kernel, rootfs, state_dir, extra_disk = sys.argv[1:7]
request = {
    "identity": {
        "requestID": "public-surface-json-request",
        "runtimeID": "public-json",
        "role": "workload",
        "backend": backend,
    },
    "config": {
        "kernelPath": kernel,
        "rootfsPath": rootfs,
        "stateDir": state_dir,
        "memoryMiB": 512,
        "cpuCount": 2,
        "disks": [
            {
                "name": "jsondata",
                "path": extra_disk,
                "mountpoint": "/jsondata",
                "mode": "ro",
            }
        ],
        "vsockListeners": [
            {
                "port": 2060,
                "target": "127.0.0.1:9",
            }
        ],
        "network": {
            "mode": "user",
            "portForwards": [
                {
                    "protocol": "tcp",
                    "host": "127.0.0.1",
                    "hostPort": 18090,
                    "guestPort": 4222,
                }
            ],
            "dns": ["1.1.1.1"],
            "routes": ["default"],
        },
    },
}
with open(path, "w", encoding="utf-8") as f:
    json.dump(request, f)
PY
"$CLI" create --json "$STATE_DIR/request.json" >"$STATE_DIR/create-json.json"
assert_json "$STATE_DIR/create-json.json" "data.get('event', {}).get('state') == 'prepared'"
assert_json "$STATE_DIR/create-json.json" "data.get('event', {}).get('identity', {}).get('requestID') == 'public-surface-json-request'"
assert_json "$STATE_DIR/create-json.json" "data.get('event', {}).get('identity', {}).get('runtimeID') == 'public-json'"
python3 - "$STATE_DIR/public-json/runtime.json" "$STATE_DIR/json-extra-disk.ext4" <<'PY'
import json
import sys

runtime_path, extra_disk = sys.argv[1:3]
with open(runtime_path, "r", encoding="utf-8") as f:
    runtime = json.load(f)
config = runtime.get("config") or {}
identity = runtime.get("event", {}).get("identity") or {}
if identity.get("requestID") != "public-surface-json-request" or identity.get("runtimeID") != "public-json":
    raise SystemExit(runtime)
if not any(disk.get("name") == "jsondata" and disk.get("path") == extra_disk and disk.get("mountpoint") == "/jsondata" and disk.get("mode") == "ro" for disk in config.get("disks", [])):
    raise SystemExit(runtime)
if not any(listener.get("port") == 2060 and listener.get("target") == "127.0.0.1:9" for listener in config.get("vsockListeners", [])):
    raise SystemExit(runtime)
network = config.get("network") or {}
if network.get("mode") != "user":
    raise SystemExit(runtime)
if not any(forward.get("host") == "127.0.0.1" and forward.get("hostPort") == 18090 and forward.get("guestPort") == 4222 for forward in network.get("portForwards", [])):
    raise SystemExit(runtime)
if network.get("dns") != ["1.1.1.1"] or network.get("routes") != ["default"]:
    raise SystemExit(runtime)
PY
python3 - "$STATE_DIR/request-stdin-create-json.json" "$backend" "$kernel_path" "$ROOTFS" "$STATE_DIR" "$JSON_STDIN_WORKSPACE" <<'PY'
import json
import sys

path, backend, kernel, rootfs, state_dir, workspace = sys.argv[1:7]
request = {
    "identity": {
        "requestID": "public-surface-json-stdin-request",
        "runtimeID": workspace,
        "role": "workload",
        "backend": backend,
    },
    "config": {
        "kernelPath": kernel,
        "rootfsPath": rootfs,
        "stateDir": state_dir,
        "memoryMiB": 512,
        "cpuCount": 2,
        "network": {"mode": "isolated"},
    },
}
with open(path, "w", encoding="utf-8") as f:
    json.dump(request, f)
    f.write("\n")
PY
"$CLI" create --json - <"$STATE_DIR/request-stdin-create-json.json" >"$STATE_DIR/create-json-stdin.json"
assert_json "$STATE_DIR/create-json-stdin.json" "data.get('event', {}).get('state') == 'prepared'"
assert_json "$STATE_DIR/create-json-stdin.json" "data.get('event', {}).get('identity', {}).get('requestID') == 'public-surface-json-stdin-request'"
assert_json "$STATE_DIR/create-json-stdin.json" "data.get('event', {}).get('identity', {}).get('runtimeID') == '$JSON_STDIN_WORKSPACE'"
python3 - "$STATE_DIR" "$backend" "$JSON_STDIN_WORKSPACE" <<'PY'
import json
import os
import sys

state_dir, backend, workspace = sys.argv[1:4]
commands = {
    "status": "public-json-stdin-status-request",
    "delete": "public-json-stdin-delete-request",
}
for command, request_id in commands.items():
    request = {
        "identity": {
            "requestID": request_id,
            "runtimeID": workspace,
            "role": "workload",
            "backend": backend,
        },
        "config": {
            "stateDir": state_dir,
        },
    }
    with open(os.path.join(state_dir, f"request-stdin-{command}-json.json"), "w", encoding="utf-8") as f:
        json.dump(request, f)
        f.write("\n")
PY
"$CLI" status --json - <"$STATE_DIR/request-stdin-status-json.json" >"$STATE_DIR/status-json-stdin-request.json"
assert_json "$STATE_DIR/status-json-stdin-request.json" "data.get('event', {}).get('identity', {}).get('requestID') == 'public-surface-json-stdin-request'"
assert_json "$STATE_DIR/status-json-stdin-request.json" "data.get('event', {}).get('identity', {}).get('runtimeID') == '$JSON_STDIN_WORKSPACE'"
assert_json "$STATE_DIR/status-json-stdin-request.json" "data.get('event', {}).get('state') == 'prepared'"
"$CLI" delete --json - <"$STATE_DIR/request-stdin-delete-json.json" >"$STATE_DIR/delete-json-stdin-request.json"
assert_json "$STATE_DIR/delete-json-stdin-request.json" "data.get('event', {}).get('identity', {}).get('requestID') == 'public-json-stdin-delete-request'"
assert_json "$STATE_DIR/delete-json-stdin-request.json" "data.get('event', {}).get('identity', {}).get('runtimeID') == '$JSON_STDIN_WORKSPACE'"
assert_json "$STATE_DIR/delete-json-stdin-request.json" "data.get('event', {}).get('state') == 'stopped'"
test ! -e "$STATE_DIR/$JSON_STDIN_WORKSPACE"
test ! -e "$STATE_DIR/workspaces/$JSON_STDIN_WORKSPACE"
python3 - "$STATE_DIR" "$backend" "$JSON_WORKSPACE" <<'PY'
import json
import os
import sys

state_dir, backend, workspace = sys.argv[1:4]
commands = {
    "status": "public-json-status-request",
    "halt": "public-json-halt-request",
    "stop": "public-json-stop-request",
    "kill": "public-json-kill-request",
    "delete": "public-json-delete-request",
}
for command, request_id in commands.items():
    request = {
        "identity": {
            "requestID": request_id,
            "runtimeID": workspace,
            "role": "workload",
            "backend": backend,
        },
        "config": {
            "stateDir": state_dir,
        },
    }
    with open(os.path.join(state_dir, f"request-{command}-json.json"), "w", encoding="utf-8") as f:
        json.dump(request, f)
        f.write("\n")
PY
"$CLI" status --json "$STATE_DIR/request-status-json.json" >"$STATE_DIR/status-json-request.json"
assert_json "$STATE_DIR/status-json-request.json" "data.get('event', {}).get('identity', {}).get('requestID') == 'public-surface-json-request'"
assert_json "$STATE_DIR/status-json-request.json" "data.get('event', {}).get('identity', {}).get('runtimeID') == '$JSON_WORKSPACE'"
assert_json "$STATE_DIR/status-json-request.json" "data.get('event', {}).get('state') == 'prepared'"
"$CLI" halt --json "$STATE_DIR/request-halt-json.json" >"$STATE_DIR/halt-json-request.json"
assert_json "$STATE_DIR/halt-json-request.json" "data.get('event', {}).get('identity', {}).get('requestID') == 'public-json-halt-request'"
assert_json "$STATE_DIR/halt-json-request.json" "data.get('event', {}).get('state') == 'halted'"
"$CLI" stop --json "$STATE_DIR/request-stop-json.json" >"$STATE_DIR/stop-json-request.json"
assert_json "$STATE_DIR/stop-json-request.json" "data.get('event', {}).get('identity', {}).get('requestID') == 'public-json-stop-request'"
assert_json "$STATE_DIR/stop-json-request.json" "data.get('event', {}).get('state') == 'stopped'"
"$CLI" kill --json "$STATE_DIR/request-kill-json.json" >"$STATE_DIR/kill-json-request.json"
assert_json "$STATE_DIR/kill-json-request.json" "data.get('event', {}).get('identity', {}).get('requestID') == 'public-json-kill-request'"
assert_json "$STATE_DIR/kill-json-request.json" "data.get('event', {}).get('state') == 'stopped'"
"$CLI" delete --json "$STATE_DIR/request-delete-json.json" >"$STATE_DIR/delete-json-request.json"
assert_json "$STATE_DIR/delete-json-request.json" "data.get('event', {}).get('identity', {}).get('requestID') == 'public-json-delete-request'"
assert_json "$STATE_DIR/delete-json-request.json" "data.get('event', {}).get('state') == 'stopped'"
test ! -e "$STATE_DIR/$JSON_WORKSPACE"
test ! -e "$STATE_DIR/workspaces/$JSON_WORKSPACE"
printf '{"identity": ' >"$STATE_DIR/request-malformed.json"
expect_failure malformed-request-json "invalid\\|unexpected\\|json" \
  "$CLI" create --json "$STATE_DIR/request-malformed.json"
printf '{"identity": ' >"$STATE_DIR/request-malformed-stdin.json"
expect_failure malformed-stdin-request-json "invalid\\|unexpected\\|json" \
  "$CLI" create --json - <"$STATE_DIR/request-malformed-stdin.json"
python3 - "$STATE_DIR/request-invalid.json" "$backend" "$kernel_path" "$ROOTFS" "$STATE_DIR" <<'PY'
import json
import sys

path, backend, kernel, rootfs, state_dir = sys.argv[1:6]
request = {
    "identity": {
        "requestID": "public-surface-invalid-json-request",
        "runtimeID": "public-json-invalid",
        "role": "workload",
        "backend": backend,
    },
    "config": {
        "kernelPath": kernel,
        "rootfsPath": rootfs,
        "stateDir": state_dir,
        "memoryMiB": 512,
        "cpuCount": 2,
        "vsockListeners": [
            {"port": 2061, "target": "127.0.0.1:9"},
            {"port": 2061, "target": "127.0.0.1:10"},
        ],
        "network": {"mode": "isolated"},
    },
}
with open(path, "w", encoding="utf-8") as f:
    json.dump(request, f)
PY
expect_failure invalid-request-combination "duplicate vsock listener port" \
  "$CLI" create --json "$STATE_DIR/request-invalid.json"

"$CLI" --json run \
  --name public-run \
  --image "$IMAGE" \
  --guest-init "$GUEST_INIT" \
  --kernel "$kernel_path" \
  --state-dir "$STATE_DIR" \
  --network isolated \
  --size-mib 96 \
  --exec "printf RUN_OK" \
  --timeout 60 >"$STATE_DIR/run.json"
assert_json "$STATE_DIR/run.json" "data.get('result', {}).get('exitCode', data.get('result', {}).get('exit_code')) == 0"
assert_json "$STATE_DIR/run.json" "'RUN_OK' in data.get('result', {}).get('stdout', '')"
assert_json "$STATE_DIR/run.json" "'RUN_OK' in data.get('serial_log', '')"
assert_json "$STATE_DIR/run.json" "'reboot: System halted' in data.get('serial_log', '') or 'reboot: Power down' in data.get('serial_log', '')"

"$CLI" --json run \
  --name public-docker-run \
  --guest-init "$GUEST_INIT" \
  --kernel "$kernel_path" \
  --state-dir "$STATE_DIR" \
  --network isolated \
  --size-mib 96 \
  --timeout 60 \
  "$IMAGE" printf DOCKER_RUN_OK >"$STATE_DIR/run-docker-style.json"
assert_json "$STATE_DIR/run-docker-style.json" "data.get('result', {}).get('exitCode', data.get('result', {}).get('exit_code')) == 0"
assert_json "$STATE_DIR/run-docker-style.json" "'DOCKER_RUN_OK' in data.get('result', {}).get('stdout', '')"

"$CLI" --json run \
  --name public-docker-image-command \
  --guest-init "$GUEST_INIT" \
  --kernel "$kernel_path" \
  --state-dir "$STATE_DIR" \
  --network isolated \
  --size-mib 96 \
  --timeout 60 \
  "$IMAGE" >"$STATE_DIR/run-docker-image-command.json"
assert_json "$STATE_DIR/run-docker-image-command.json" "data.get('result', {}).get('exitCode', data.get('result', {}).get('exit_code')) == 0"

"$CLI" --json run \
  --name "$RUN_KEEP_WORKSPACE" \
  --image "$IMAGE" \
  --guest-init "$GUEST_INIT" \
  --kernel "$kernel_path" \
  --state-dir "$STATE_DIR" \
  --network isolated \
  --size-mib 96 \
  --exec "printf RUN_KEEP_OK; printf keep-state > /tmp/keep-state.txt" \
  --output keep-state=/tmp/keep-state.txt \
  --keep \
  --timeout 60 >"$STATE_DIR/run-keep.json"
assert_json "$STATE_DIR/run-keep.json" "data.get('final_state') == 'stopped'"
assert_json "$STATE_DIR/run-keep.json" "data.get('result', {}).get('exitCode', data.get('result', {}).get('exit_code')) == 0"
assert_json "$STATE_DIR/run-keep.json" "'RUN_KEEP_OK' in data.get('result', {}).get('stdout', '')"
test -e "$STATE_DIR/workspaces/$RUN_KEEP_WORKSPACE/rootfs.ext4"
test -e "$STATE_DIR/$RUN_KEEP_WORKSPACE/result.json"
"$CLI" --json status "$RUN_KEEP_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-run-keep.json"
assert_json "$STATE_DIR/status-run-keep.json" "data.get('event', {}).get('state') == 'stopped'"
assert_json "$STATE_DIR/status-run-keep.json" "data.get('readiness', {}).get('resultReady', {}).get('ready') is True"
"$CLI" --text status "$RUN_KEEP_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-run-keep-text.txt"
grep -q "Workspace: $RUN_KEEP_WORKSPACE" "$STATE_DIR/status-run-keep-text.txt"
grep -q "State: stopped" "$STATE_DIR/status-run-keep-text.txt"
grep -q "Readiness:" "$STATE_DIR/status-run-keep-text.txt"
"$CLI" inspect "$RUN_KEEP_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/inspect-run-keep.json"
assert_json "$STATE_DIR/inspect-run-keep.json" "data.get('event', {}).get('identity', {}).get('runtimeID') == '$RUN_KEEP_WORKSPACE' and data.get('event', {}).get('state') == 'stopped'"
"$CLI" --json result "$RUN_KEEP_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/result-run-keep.json"
assert_json "$STATE_DIR/result-run-keep.json" "'RUN_KEEP_OK' in data.get('result', {}).get('stdout', '')"
"$CLI" --output text result "$RUN_KEEP_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/result-run-keep-text.txt"
grep -q "Workspace: $RUN_KEEP_WORKSPACE" "$STATE_DIR/result-run-keep-text.txt"
grep -q "Exit code: 0" "$STATE_DIR/result-run-keep-text.txt"
grep -q "RUN_KEEP_OK" "$STATE_DIR/result-run-keep-text.txt"
"$CLI" logs "$RUN_KEEP_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/logs-run-keep.txt"
grep -q "RUN_KEEP_OK" "$STATE_DIR/logs-run-keep.txt"
"$CLI" --text logs "$RUN_KEEP_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/logs-run-keep-text.txt"
grep -q "RUN_KEEP_OK" "$STATE_DIR/logs-run-keep-text.txt"
mkdir -p "$STATE_DIR/run-keep-artifacts"
"$CLI" artifacts get "$RUN_KEEP_WORKSPACE" keep-state "$STATE_DIR/run-keep-artifacts" --state-dir "$STATE_DIR" >"$STATE_DIR/artifact-run-keep.json"
grep -q "keep-state" "$STATE_DIR/run-keep-artifacts/keep-state.txt"
"$CLI" delete "$RUN_KEEP_WORKSPACE" --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete-run-keep.json"
test ! -e "$STATE_DIR/workspaces/$RUN_KEEP_WORKSPACE"

cat >"$STATE_DIR/options-setup.sh" <<'SH'
mkdir -p /tmp/microagent-options
printf setup-file-ok > /tmp/microagent-options/setup.txt
SH
"$CLI" --json run \
  --name "$OPTIONS_RUN_WORKSPACE" \
  --image "$IMAGE" \
  --guest-init "$GUEST_INIT" \
  --kernel "$kernel_path" \
  --state-dir "$STATE_DIR" \
  --network isolated \
  --size-mib 96 \
  --setup-file "$STATE_DIR/options-setup.sh" \
  --shell /bin/sh \
  --hostname options-host \
  --env OPTION_ENV=cli-env \
  --exec "test \"\$(hostname)\" = options-host; test \"\$OPTION_ENV\" = cli-env; grep -q setup-file-ok /tmp/microagent-options/setup.txt; printf OPTIONS_RUN_OK" \
  --timeout 60 >"$STATE_DIR/options-run.json"
assert_json "$STATE_DIR/options-run.json" "data.get('result', {}).get('exitCode', data.get('result', {}).get('exit_code')) == 0"
assert_json "$STATE_DIR/options-run.json" "'OPTIONS_RUN_OK' in data.get('result', {}).get('stdout', '')"

"$CLI" --json create "$SERVICE_WORKSPACE" \
  --image "$IMAGE" \
  --guest-init "$GUEST_INIT" \
  --kernel "$kernel_path" \
  --state-dir "$STATE_DIR" \
  --network isolated \
  --size-mib 96 \
  --service-command "printf '{\"ok\":true,\"service\":\"options\",\"env\":\"%s\",\"hostname\":\"%s\"}' \"\$SERVICE_ENV\" \"\$(hostname)\" > /service-options-report.json; sync; sleep 300" \
  --shell /bin/sh \
  --hostname service-host \
  --env SERVICE_ENV=service-env \
  --output service-report=/service-options-report.json >"$STATE_DIR/create-service-options.json"
assert_json "$STATE_DIR/create-service-options.json" "data.get('workspace') == '$SERVICE_WORKSPACE'"
assert_json "$STATE_DIR/create-service-options.json" "data.get('shell') == '/bin/sh' and data.get('hostname') == 'service-host'"
"$CLI" start "$SERVICE_WORKSPACE" --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/start-service-options.json"
wait_for_status_ready "$SERVICE_WORKSPACE" "$STATE_DIR/status-service-options-running.json"
sleep 5
"$CLI" halt "$SERVICE_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt-service-options.json"
mkdir -p "$STATE_DIR/service-options-artifacts"
"$CLI" artifacts get "$SERVICE_WORKSPACE" service-report "$STATE_DIR/service-options-artifacts" --state-dir "$STATE_DIR" >"$STATE_DIR/artifact-service-options.json"
assert_json "$STATE_DIR/service-options-artifacts/service-options-report.json" "data == {'ok': True, 'service': 'options', 'env': 'service-env', 'hostname': 'service-host'}"
"$CLI" delete "$SERVICE_WORKSPACE" --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete-service-options.json"

"$CLI" --json create "$IMAGE_COMMAND_WORKSPACE" \
  --image "${MICROAGENT_NATS_IMAGE:-docker.io/library/nats:2.10.26-alpine}" \
  --image-command \
  --guest-init "$GUEST_INIT" \
  --kernel "$kernel_path" \
  --state-dir "$STATE_DIR" \
  --network isolated \
  --size-mib 192 \
  --shell /bin/sh >"$STATE_DIR/create-image-command.json"
assert_json "$STATE_DIR/create-image-command.json" "data.get('workspace') == '$IMAGE_COMMAND_WORKSPACE'"
"$CLI" start "$IMAGE_COMMAND_WORKSPACE" --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/start-image-command.json"
wait_for_status_ready "$IMAGE_COMMAND_WORKSPACE" "$STATE_DIR/status-image-command-running.json"
sleep 5
"$CLI" logs "$IMAGE_COMMAND_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/logs-image-command.txt"
grep -Eiq "nats-server|server is ready" "$STATE_DIR/logs-image-command.txt"
"$CLI" halt "$IMAGE_COMMAND_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt-image-command.json"
"$CLI" delete "$IMAGE_COMMAND_WORKSPACE" --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete-image-command.json"

"$CLI" --json create "$WORKSPACE" \
  --image "$IMAGE" \
  --guest-init "$GUEST_INIT" \
  --kernel "$kernel_path" \
  --state-dir "$STATE_DIR" \
  --network isolated \
  --size-mib 96 \
  --entrypoint "printf RESULT_OK; printf RESULT_ERR >&2" \
  --output report=/tmp/report.txt >"$STATE_DIR/create-result.json"
"$CLI" start "$WORKSPACE" --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/start-result.json"
wait_for_result "$WORKSPACE" "$STATE_DIR/result.json"
assert_json "$STATE_DIR/result.json" "data.get('result', {}).get('exitCode', data.get('result', {}).get('exit_code')) == 0"
assert_json "$STATE_DIR/result.json" "'RESULT_OK' in data.get('result', {}).get('stdout', '')"
assert_json "$STATE_DIR/result.json" "'RESULT_ERR' in data.get('result', {}).get('stderr', '')"
"$CLI" stop "$WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/stop-result.json" || true
"$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete-result.json"

"$CLI" --json create missing-result \
  --image "$IMAGE" \
  --guest-init "$GUEST_INIT" \
  --kernel "$kernel_path" \
  --state-dir "$STATE_DIR" \
  --network isolated \
  --size-mib 96 \
  --entrypoint "sleep 300" >"$STATE_DIR/create-missing-result.json"
expect_failure missing-result "result" \
  "$CLI" --json result missing-result --state-dir "$STATE_DIR"
printf '{not-json\n' >"$STATE_DIR/missing-result/result.json"
expect_failure malformed-result "invalid\\|character\\|json" \
  "$CLI" --json result missing-result --state-dir "$STATE_DIR"
"$CLI" delete missing-result --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete-missing-result.json"

mkdir -p "$STATE_DIR/bundle-src"
printf "bundle-seed\n" >"$STATE_DIR/bundle-src/seed.txt"
tar -C "$STATE_DIR/bundle-src" -cf "$STATE_DIR/bundle.tar" .
"$CLI" --json create "$BUNDLE_WORKSPACE" \
  --image "$IMAGE" \
  --guest-init "$GUEST_INIT" \
  --kernel "$kernel_path" \
  --state-dir "$STATE_DIR" \
  --network isolated \
  --size-mib 96 \
  -v "$STATE_DIR/bundle.tar:/data:rw" \
  --output disk-report=/data/report.txt \
  --output missing-report=/data/missing.txt >"$STATE_DIR/create-bundle.json"
assert_json "$STATE_DIR/create-bundle.json" "any(disk.get('name') == 'data' and disk.get('bundle') for disk in data.get('disks', []))"
"$CLI" start "$BUNDLE_WORKSPACE" --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/start-bundle.json"
wait_for_status_ready "$BUNDLE_WORKSPACE" "$STATE_DIR/status-bundle-running.json"
"$CLI" connect "$BUNDLE_WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "cat /data/seed.txt > /data/report.txt; printf ':written' >> /data/report.txt; sync" \
  --ready-timeout 30 \
  --timeout 10 >"$STATE_DIR/connect-bundle.txt"
"$CLI" halt "$BUNDLE_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt-bundle.json"
"$CLI" --json artifacts "$BUNDLE_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/artifacts-bundle.json"
assert_json "$STATE_DIR/artifacts-bundle.json" "any(item.get('name') == 'disk-report' for item in data.get('artifacts', {}).get('egress', []))"
mkdir -p "$ARTIFACT_DIR"
"$CLI" artifacts get "$BUNDLE_WORKSPACE" disk-report "$ARTIFACT_DIR" --state-dir "$STATE_DIR" >"$STATE_DIR/artifact-bundle.json"
grep -q "bundle-seed" "$ARTIFACT_DIR/report.txt"
grep -q ":written" "$ARTIFACT_DIR/report.txt"
expect_failure missing-artifact-file "missing.txt\\|not found\\|No such" \
  "$CLI" artifacts get "$BUNDLE_WORKSPACE" missing-report "$ARTIFACT_DIR" --state-dir "$STATE_DIR"
"$CLI" cp "$BUNDLE_WORKSPACE:data:/report.txt" "$STATE_DIR/copied-report.txt" --state-dir "$STATE_DIR" >"$STATE_DIR/cp-attached-disk.json"
grep -q "bundle-seed" "$STATE_DIR/copied-report.txt"
"$CLI" rm "$BUNDLE_WORKSPACE" --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete-bundle.json"

mkdir -p "$STATE_DIR/disk-src/dir"
printf "existing-disk-seed\n" >"$STATE_DIR/disk-src/seed.txt"
truncate -s 64M "$STATE_DIR/existing-disk.ext4"
mke2fs -q -F -t ext4 -d "$STATE_DIR/disk-src" "$STATE_DIR/existing-disk.ext4"
"$CLI" --json create "$DISK_WORKSPACE" \
  --image "$IMAGE" \
  --guest-init "$GUEST_INIT" \
  --kernel "$kernel_path" \
  --state-dir "$STATE_DIR" \
  --network isolated \
  --size-mib 96 \
  --disk "workspace=$STATE_DIR/existing-disk.ext4:/workspace:rw" \
  --disk "readonly=$STATE_DIR/existing-disk.ext4:/readonly:ro" \
  --output existing-disk-report=/workspace/report.txt >"$STATE_DIR/create-existing-disk.json"
assert_json "$STATE_DIR/create-existing-disk.json" "any(disk.get('name') == 'workspace' and not disk.get('bundle') for disk in data.get('disks', []))"
"$CLI" start "$DISK_WORKSPACE" --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/start-existing-disk.json"
wait_for_status_ready "$DISK_WORKSPACE" "$STATE_DIR/status-existing-disk-running.json"
"$CLI" connect "$DISK_WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --send "cat /workspace/seed.txt > /workspace/report.txt; sync" \
  --ready-timeout 30 \
  --timeout 10 >"$STATE_DIR/connect-existing-disk.txt"
"$CLI" halt "$DISK_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/halt-existing-disk.json"
mkdir -p "$STATE_DIR/existing-disk-artifacts"
"$CLI" artifacts get "$DISK_WORKSPACE" existing-disk-report "$STATE_DIR/existing-disk-artifacts" --state-dir "$STATE_DIR" >"$STATE_DIR/artifact-existing-disk.json"
grep -q "existing-disk-seed" "$STATE_DIR/existing-disk-artifacts/report.txt"
dd if=/dev/zero of="$STATE_DIR/large.bin" bs=1024 count=1024 status=none
"$CLI" cp "$STATE_DIR/large.bin" "$DISK_WORKSPACE:workspace:/large.bin" --state-dir "$STATE_DIR" >"$STATE_DIR/cp-large-to-disk.json"
"$CLI" cp "$DISK_WORKSPACE:workspace:/large.bin" "$STATE_DIR/large-out.bin" --state-dir "$STATE_DIR" >"$STATE_DIR/cp-large-from-disk.json"
cmp "$STATE_DIR/large.bin" "$STATE_DIR/large-out.bin"
expect_failure cp-directory-source "regular file" \
  "$CLI" cp "$STATE_DIR/disk-src/dir" "$DISK_WORKSPACE:workspace:/dir-copy" --state-dir "$STATE_DIR"
printf "space-name\n" >"$STATE_DIR/space name.txt"
expect_failure cp-whitespace-source "whitespace" \
  "$CLI" cp "$STATE_DIR/space name.txt" "$DISK_WORKSPACE:workspace:/space-name.txt" --state-dir "$STATE_DIR"
printf "host-copy\n" >"$STATE_DIR/host-copy.txt"
printf "odd-name\n" >"$STATE_DIR/odd-name.txt"
"$CLI" cp "$STATE_DIR/odd-name.txt" "$DISK_WORKSPACE:workspace:/odd-!@%+=,.txt" --state-dir "$STATE_DIR" >"$STATE_DIR/cp-odd-name-to-disk.json"
"$CLI" cp "$DISK_WORKSPACE:workspace:/odd-!@%+=,.txt" "$STATE_DIR/odd-name-out.txt" --state-dir "$STATE_DIR" >"$STATE_DIR/cp-odd-name-from-disk.json"
grep -q "odd-name" "$STATE_DIR/odd-name-out.txt"
expect_failure cp-missing-parent "parent .*does not exist\\|File not found" \
  "$CLI" cp "$STATE_DIR/host-copy.txt" "$DISK_WORKSPACE:workspace:/missing-parent/file.txt" --state-dir "$STATE_DIR"
ln -s host-copy.txt "$STATE_DIR/host-copy-link"
"$CLI" cp "$STATE_DIR/host-copy-link" "$DISK_WORKSPACE:workspace:/link-source.txt" --state-dir "$STATE_DIR" >"$STATE_DIR/cp-link-source-to-disk.json"
"$CLI" cp "$DISK_WORKSPACE:workspace:/link-source.txt" "$STATE_DIR/link-source-out.txt" --state-dir "$STATE_DIR" >"$STATE_DIR/cp-link-source-from-disk.json"
grep -q "host-copy" "$STATE_DIR/link-source-out.txt"
printf "private-copy\n" >"$STATE_DIR/private-copy.txt"
chmod 600 "$STATE_DIR/private-copy.txt"
"$CLI" cp "$STATE_DIR/private-copy.txt" "$DISK_WORKSPACE:workspace:/private-copy.txt" --state-dir "$STATE_DIR" >"$STATE_DIR/cp-private-to-disk.json"
"$CLI" cp "$DISK_WORKSPACE:workspace:/private-copy.txt" "$STATE_DIR/private-copy-out.txt" --state-dir "$STATE_DIR" >"$STATE_DIR/cp-private-from-disk.json"
grep -q "private-copy" "$STATE_DIR/private-copy-out.txt"
expect_failure cp-readonly-disk "read-only" \
  "$CLI" cp "$STATE_DIR/host-copy.txt" "$DISK_WORKSPACE:readonly:/blocked.txt" --state-dir "$STATE_DIR"
mv "$STATE_DIR/existing-disk.ext4" "$STATE_DIR/existing-disk.ext4.unavailable"
expect_failure unavailable-disk-artifact "No such\\|not found\\|open" \
  "$CLI" artifacts get "$DISK_WORKSPACE" existing-disk-report "$STATE_DIR/unavailable-disk-artifacts" --state-dir "$STATE_DIR"
mv "$STATE_DIR/existing-disk.ext4.unavailable" "$STATE_DIR/existing-disk.ext4"
cp "$STATE_DIR/workspaces/$DISK_WORKSPACE/workspace.json" "$STATE_DIR/workspace-json.backup"
printf "{not-json\n" >"$STATE_DIR/workspaces/$DISK_WORKSPACE/workspace.json"
expect_failure corrupt-workspace-artifact "invalid character\\|workspace.json\\|json" \
  "$CLI" artifacts get "$DISK_WORKSPACE" existing-disk-report "$STATE_DIR/corrupt-workspace-artifacts" --state-dir "$STATE_DIR"
mv "$STATE_DIR/workspace-json.backup" "$STATE_DIR/workspaces/$DISK_WORKSPACE/workspace.json"
"$CLI" delete "$DISK_WORKSPACE" --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete-existing-disk.json"

"$CLI" --json create "$PERF_WORKSPACE" \
  --image "$IMAGE" \
  --guest-init "$GUEST_INIT" \
  --kernel "$kernel_path" \
  --state-dir "$STATE_DIR" \
  --network isolated \
  --memory 768 \
  --cpus 1 \
  --size-mib 96 \
  --entrypoint "sleep 300" >"$STATE_DIR/create-perf.json"
assert_json "$STATE_DIR/create-perf.json" "data.get('resources', {}).get('memory_mib', data.get('resources', {}).get('memoryMiB')) == 768"
assert_json "$STATE_DIR/create-perf.json" "data.get('resources', {}).get('cpu_count', data.get('resources', {}).get('cpuCount')) == 1"
"$CLI" --json start "$PERF_WORKSPACE" --state-dir "$STATE_DIR" --kernel "$kernel_path" >"$STATE_DIR/start-perf.json"
assert_json "$STATE_DIR/start-perf.json" "data.get('resources', {}).get('memory_mib', data.get('resources', {}).get('memoryMiB')) == 768"
assert_json "$STATE_DIR/start-perf.json" "data.get('resources', {}).get('cpu_count', data.get('resources', {}).get('cpuCount')) == 1"
wait_for_status_ready "$PERF_WORKSPACE" "$STATE_DIR/status-perf-running.json"
python3 - "$STATE_DIR/$PERF_WORKSPACE/runtime.json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    runtime = json.load(f)
config = runtime.get("config") or {}
if config.get("memoryMiB") != 768 or config.get("cpuCount") != 1:
    raise SystemExit(runtime)
PY
"$CLI" --json perf footprint "$PERF_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/perf-footprint.json"
assert_json "$STATE_DIR/perf-footprint.json" "data.get('rss_kib', 0) > 0 and data.get('state') == 'running'"
"$CLI" --json perf steady "$PERF_WORKSPACE" --state-dir "$STATE_DIR" --duration 1 --interval 1 >"$STATE_DIR/perf-steady.json"
assert_json "$STATE_DIR/perf-steady.json" "data.get('summary', {}).get('count', 0) >= 1"
expect_failure perf-boot-zero-iterations "iterations must be positive" \
  "$CLI" --json perf boot \
  --image "$IMAGE" \
  --profile tiny \
  --state-dir "$STATE_DIR/perf-zero-iterations" \
  --exec "true" \
  --iterations 0 \
  --timeout 90
expect_failure perf-boot-zero-timeout "timeout must be positive" \
  "$CLI" --json perf boot \
  --image "$IMAGE" \
  --profile tiny \
  --state-dir "$STATE_DIR/perf-zero-timeout" \
  --exec "true" \
  --iterations 1 \
  --timeout 0
"$CLI" --json perf boot \
  --image "$IMAGE" \
  --profile definitely-not-a-profile \
  --state-dir "$STATE_DIR/perf-invalid-profile" \
  --exec "true" \
  --iterations 1 \
  --timeout 90 >"$STATE_DIR/perf-invalid-profile.json"
assert_json "$STATE_DIR/perf-invalid-profile.json" "data.get('summary', {}).get('count') == 1"
assert_json "$STATE_DIR/perf-invalid-profile.json" "data.get('iterations', [])[0].get('ok') is False"
assert_json "$STATE_DIR/perf-invalid-profile.json" "'unknown resource profile' in data.get('iterations', [])[0].get('error', '')"
"$CLI" --json perf boot \
  --image "$IMAGE" \
  --profile tiny \
  --state-dir "$STATE_DIR" \
  --exec "printf PERF_BOOT_OK" \
  --iterations 2 \
  --timeout 90 >"$STATE_DIR/perf-boot.json"
assert_json "$STATE_DIR/perf-boot.json" "data.get('benchmark') == 'boot'"
assert_json "$STATE_DIR/perf-boot.json" "data.get('summary', {}).get('count') == 2"
assert_json "$STATE_DIR/perf-boot.json" "len(data.get('iterations', [])) == 2"
assert_json "$STATE_DIR/perf-boot.json" "all(item.get('ok') is True for item in data.get('iterations', []))"
assert_json "$STATE_DIR/perf-boot.json" "all(item.get('duration_ms', 0) > 0 for item in data.get('iterations', []))"
assert_json "$STATE_DIR/perf-boot.json" "data.get('summary', {}).get('min_ms', 0) > 0 and data.get('summary', {}).get('avg_ms', 0) > 0 and data.get('summary', {}).get('max_ms', 0) >= data.get('summary', {}).get('min_ms', 0)"
"$CLI" kill "$PERF_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/kill-perf.json"
"$CLI" --json status "$PERF_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/status-perf-killed.json"
assert_json "$STATE_DIR/status-perf-killed.json" "data.get('event', {}).get('state') == 'stopped'"
"$CLI" --json stop "$PERF_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/stop-perf-again.json"
assert_json "$STATE_DIR/stop-perf-again.json" "data.get('event', {}).get('state') == 'stopped'"
"$CLI" --json kill "$PERF_WORKSPACE" --state-dir "$STATE_DIR" >"$STATE_DIR/kill-perf-again.json"
assert_json "$STATE_DIR/kill-perf-again.json" "data.get('event', {}).get('state') == 'stopped'"
"$CLI" delete "$PERF_WORKSPACE" --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete-perf.json"
"$CLI" --json delete "$PERF_WORKSPACE" --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete-perf-again.json"
assert_json "$STATE_DIR/delete-perf-again.json" "data.get('event', {}).get('state') == 'stopped'"

mkdir -p "$STATE_DIR/corrupt-state"
printf '{not-json\n' >"$STATE_DIR/corrupt-state/event.json"
expect_failure corrupt-state "invalid\\|character\\|json" \
  "$CLI" status corrupt-state --state-dir "$STATE_DIR"

python3 - "$STATE_DIR" "$backend" <<'PY'
import json
import os
import sys
from datetime import datetime, timezone

state_dir, backend = sys.argv[1:3]
observed_at = datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")

def write_event(name):
    path = os.path.join(state_dir, name)
    os.makedirs(path, exist_ok=True)
    event = {
        "identity": {
            "requestID": f"{name}-request",
            "runtimeID": name,
            "role": "workload",
            "backend": backend,
        },
        "state": "stopped",
        "detail": "synthetic stopped event for state corruption E2E",
        "observedAt": observed_at,
    }
    with open(os.path.join(path, "event.json"), "w", encoding="utf-8") as f:
        json.dump(event, f)
        f.write("\n")

write_event("missing-runtime")
write_event("malformed-runtime")
with open(os.path.join(state_dir, "malformed-runtime", "runtime.json"), "w", encoding="utf-8") as f:
    f.write("{not-json\n")
os.makedirs(os.path.join(state_dir, "workspaces", "partial-state"), exist_ok=True)
PY
"$CLI" --json status missing-runtime --state-dir "$STATE_DIR" >"$STATE_DIR/status-missing-runtime.json"
assert_json "$STATE_DIR/status-missing-runtime.json" "data.get('event', {}).get('state') == 'stopped'"
assert_json "$STATE_DIR/status-missing-runtime.json" "data.get('readiness', {}).get('guestReady', {}).get('ready') is True"
"$CLI" --json status malformed-runtime --state-dir "$STATE_DIR" >"$STATE_DIR/status-malformed-runtime.json"
assert_json "$STATE_DIR/status-malformed-runtime.json" "data.get('event', {}).get('state') == 'stopped'"
expect_failure malformed-runtime-start "invalid\\|character\\|json" \
  "$CLI" start malformed-runtime --state-dir "$STATE_DIR" --kernel "$kernel_path"
"$CLI" --json ps --state-dir "$STATE_DIR" >"$STATE_DIR/ps-partial-state.json"
assert_json "$STATE_DIR/ps-partial-state.json" "any(item.get('name') == 'partial-state' and item.get('state') == 'unknown' for item in data.get('workspaces', []))"
"$CLI" --json delete partial-state --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete-partial-state.json"
assert_json "$STATE_DIR/delete-partial-state.json" "data.get('event', {}).get('state') == 'stopped'"
test ! -e "$STATE_DIR/workspaces/partial-state"
"$CLI" --json delete partial-state --yes --state-dir "$STATE_DIR" >"$STATE_DIR/delete-partial-state-again.json"
assert_json "$STATE_DIR/delete-partial-state-again.json" "data.get('event', {}).get('state') == 'stopped'"

echo "microagent public surface E2E passed"
