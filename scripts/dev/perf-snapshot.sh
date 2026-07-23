#!/usr/bin/env bash
set -euo pipefail

# perf-snapshot.sh: measure microagent boot time and workspace footprint on
# THIS host and print one paste-able summary block (including host context)
# suitable for a bug report, a PR description, or a docs "measured numbers"
# table. It is not a pass/fail gate: it exists to make repeatable, honestly
# host-labeled measurements a one-command operation.
#
# Usage:
#   scripts/dev/perf-snapshot.sh
#   MICROAGENT_CLI=/path/to/installed/microagent scripts/dev/perf-snapshot.sh
#
# Env overrides:
#   MICROAGENT_CLI          - use an already-installed microagent binary
#                             instead of building one from this checkout
#   MICROAGENT_FIRECRACKER  - explicit firecracker binary (Linux); otherwise
#                             resolved the same way the E2E suite resolves it
#   MICROAGENT_NATS_IMAGE   - override the pinned measurement image
#   PERF_ITERATIONS         - boot iterations (default 10)
#   MICROAGENT_KEEP_PERF_SNAPSHOT=1 - keep the scratch state dir on success

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

ITERATIONS="${PERF_ITERATIONS:-10}"
IMAGE="${MICROAGENT_NATS_IMAGE:-docker.io/library/nats@sha256:6e0cca2c6da79f0a3542ec5a3319dd10b1b05f5d8e8949afa8e9cdf6314bbf6c}"
WORKSPACE="perf-snapshot"

STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-perf-snapshot.XXXXXX")"
BUILD_DIR="$STATE_DIR/build"
mkdir -p "$BUILD_DIR"

cleanup() {
  status="$?"
  if [ -n "${CLI:-}" ] && [ -x "$CLI" ]; then
    "$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_PERF_SNAPSHOT:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent perf-snapshot state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

if [ -n "${MICROAGENT_CLI:-}" ]; then
  CLI="$MICROAGENT_CLI"
  [ -x "$CLI" ] || e2e_fail "MICROAGENT_CLI is not executable: $CLI"
else
  CLI="$BUILD_DIR/microagent"
  e2e_build_cli "$CLI"
fi

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    e2e_have_kvm || e2e_skip "/dev/kvm is not visible; run this on a host with KVM"
    if [ -z "${MICROAGENT_FIRECRACKER:-}" ]; then
      resolved="$("$CLI" --json doctor 2>/dev/null | sed -n 's/.*"binaryPath"[: ]*"\([^"]*\)".*/\1/p' | head -1)"
      if [ -z "$resolved" ]; then
        resolved="$(e2e_resolve_firecracker || true)"
      fi
      [ -n "$resolved" ] || e2e_skip "no firecracker binary found; install microagent or set MICROAGENT_FIRECRACKER"
      export MICROAGENT_FIRECRACKER="$resolved"
    fi
    ;;
  Darwin:arm64)
    e2e_have_applevf || e2e_skip "Apple VF supervisor unavailable; install microagent (brew) or build the supervisor first"
    ;;
  *)
    e2e_skip "perf-snapshot requires Linux amd64 (KVM) or macOS arm64 (Apple Virtualization.framework)"
    ;;
esac

e2e_step "perf boot ($ITERATIONS iterations, image=$IMAGE)"
BOOT_JSON="$STATE_DIR/boot.json"
"$CLI" --json perf boot \
  --image "$IMAGE" \
  --profile tiny \
  --network isolated \
  --iterations "$ITERATIONS" \
  --state-dir "$STATE_DIR" | tee "$BOOT_JSON" >/dev/null

e2e_step "perf footprint (persistent workspace, image=$IMAGE)"
"$CLI" --json create "$WORKSPACE" \
  --image "$IMAGE" \
  --profile tiny \
  --network isolated \
  --state-dir "$STATE_DIR" >/dev/null
"$CLI" --json start "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null
FOOTPRINT_JSON="$STATE_DIR/footprint.json"
"$CLI" --json perf footprint "$WORKSPACE" --state-dir "$STATE_DIR" | tee "$FOOTPRINT_JSON" >/dev/null
"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
"$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" >/dev/null 2>&1 || true

e2e_step "summary"

MICROAGENT_VERSION="$("$CLI" --json version 2>/dev/null | sed -n 's/.*"version"[: ]*"\([^"]*\)".*/\1/p')"
SOURCE_COMMIT="$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || echo unknown)"
OS_NAME="$(uname -s)"
KERNEL_RELEASE="$(uname -r)"
ARCH="$(uname -m)"
WSL_NOTE=""
if e2e_is_wsl; then
  WSL_NOTE=" (WSL2 - not a native Linux host; timings are not comparable to bare-metal or macOS)"
fi

if [ "$OS_NAME" = "Darwin" ]; then
  CPU_MODEL="$(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo unknown)"
  RAM_TOTAL="$(( $(sysctl -n hw.memsize 2>/dev/null || echo 0) / 1024 / 1024 / 1024 ))GiB"
else
  CPU_MODEL="$(sed -n 's/^model name[[:space:]]*: //p' /proc/cpuinfo 2>/dev/null | head -1)"
  [ -n "$CPU_MODEL" ] || CPU_MODEL="unknown"
  RAM_TOTAL="$(free -h 2>/dev/null | awk '/^Mem:/{print $2}')"
  [ -n "$RAM_TOTAL" ] || RAM_TOTAL="unknown"
fi

python3 - "$BOOT_JSON" "$FOOTPRINT_JSON" \
  "$CPU_MODEL" "$ARCH" "$RAM_TOTAL" "$OS_NAME" "$KERNEL_RELEASE" "$WSL_NOTE" \
  "$MICROAGENT_VERSION" "$SOURCE_COMMIT" <<'PY'
import json
import sys

(boot_path, footprint_path, cpu_model, arch, ram_total, os_name,
 kernel_release, wsl_note, microagent_version, source_commit) = sys.argv[1:]

with open(boot_path) as f:
    boot = json.load(f)
with open(footprint_path) as f:
    footprint = json.load(f)

s = boot.get("summary", {})
rss_kib = footprint.get("rss_kib")
rss_mib = f"{rss_kib / 1024:.1f} MiB" if isinstance(rss_kib, (int, float)) else "n/a"

print()
print("=== microagent perf snapshot ===")
print(f"host: {cpu_model} | {arch} | RAM {ram_total}")
print(f"os: {os_name} {kernel_release}{wsl_note}")
print(f"microagent: version={microagent_version} commit={source_commit}")
print(f"backend: {boot.get('backend', 'unknown')}")
print(f"image: {boot.get('image_ref', 'unknown')} profile={boot.get('profile', 'tiny')} network=isolated")
print()
print(f"boot      iterations={s.get('count')} failures={s.get('failures')} "
      f"min={s.get('min_ms')}ms avg={s.get('avg_ms')}ms max={s.get('max_ms')}ms")
print(f"footprint rss={rss_mib} ({rss_kib} KiB)")
print("=================================")
PY
