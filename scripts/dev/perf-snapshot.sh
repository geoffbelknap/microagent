#!/usr/bin/env bash
set -euo pipefail

# perf-snapshot.sh: measure microagent's full-readiness lifecycle matrix,
# legacy boot timing, and workspace footprint on THIS host. It prints one
# paste-able summary block (including host context, timing boundaries, and
# exact component paths and hashes) suitable for a bug report, a PR
# description, or a docs "measured numbers" table. It is not a latency
# pass/fail gate: it exists to make repeatable, honestly host-labeled
# measurements a one-command operation. A checkout-built run does fail if the
# CLI resolves a guest init or supervisor other than the matched build.
#
# Usage:
#   scripts/dev/perf-snapshot.sh
#   MICROAGENT_CLI=/path/to/installed/microagent scripts/dev/perf-snapshot.sh
#
# Without MICROAGENT_CLI, the script builds the CLI, guest init, and host
# supervisor from this checkout as one matched stack. Measuring a source-built
# CLI against an older installed companion produces a mixed-version result.
#
# Env overrides:
#   MICROAGENT_CLI          - use an already-installed microagent binary
#                             instead of building one from this checkout; the
#                             summary labels it external and hashes the
#                             companions it actually resolves
#   MICROAGENT_FIRECRACKER  - explicit firecracker binary (Linux); otherwise
#                             resolved the same way the E2E suite resolves it
#   MICROAGENT_NATS_IMAGE   - override the pinned measurement image
#   PERF_NETWORK            - network mode measured in every lane
#                             (default isolated)
#   PERF_ITERATIONS         - iterations per boot/readiness lane (default 10)
#   PERF_READY_TIMEOUT      - timeout per readiness iteration (default 90s)
#   PERF_STATE_PARENT       - disk-backed parent for scratch state
#                             (default /var/tmp)
#   MICROAGENT_KEEP_PERF_SNAPSHOT=1 - keep the scratch state dir on success

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

ITERATIONS="${PERF_ITERATIONS:-10}"
READY_TIMEOUT="${PERF_READY_TIMEOUT:-90}"
IMAGE="${MICROAGENT_NATS_IMAGE:-docker.io/library/nats@sha256:6e0cca2c6da79f0a3542ec5a3319dd10b1b05f5d8e8949afa8e9cdf6314bbf6c}"
NETWORK="${PERF_NETWORK:-isolated}"
WORKSPACE="perf-snapshot"

STATE_PARENT="${PERF_STATE_PARENT:-/var/tmp}"
mkdir -p "$STATE_PARENT"
STATE_DIR="$(mktemp -d "$STATE_PARENT/microagent-perf-snapshot.XXXXXX")"
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
  CLI_SOURCE="external"
  [ -x "$CLI" ] || e2e_fail "MICROAGENT_CLI is not executable: $CLI"
else
  CLI="$BUILD_DIR/microagent"
  CLI_SOURCE="checkout"
  "$ROOT/scripts/dev/build-local.sh" --output "$CLI" --quiet
fi

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    e2e_have_kvm || e2e_skip "/dev/kvm is not visible; run this on a host with KVM"
    if [ -z "${MICROAGENT_FIRECRACKER:-}" ]; then
      # A source-built stack may not have a sibling VMM yet. Doctor then exits
      # nonzero after still emitting its structured host report; do not let
      # pipefail abort before the installed-binary fallback can run.
      resolved="$("$CLI" --json doctor 2>/dev/null | sed -n 's/.*"binaryPath"[: ]*"\([^"]*\)".*/\1/p' | head -1 || true)"
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
  --network "$NETWORK" \
  --iterations "$ITERATIONS" \
  --state-dir "$STATE_DIR" | tee "$BOOT_JSON" >/dev/null

e2e_step "full-readiness matrix ($ITERATIONS iterations per lane, image=$IMAGE)"
for start_mode in cold snapshot-fork snapshot-restore paused-resume; do
  for probe_mode in exec interactive; do
    READY_JSON="$STATE_DIR/ready-$start_mode-$probe_mode.json"
    "$CLI" --json perf ready \
      --image "$IMAGE" \
      --profile tiny \
      --network "$NETWORK" \
      --start "$start_mode" \
      --probe "$probe_mode" \
      --exec "printf PERF_READY_OK" \
      --iterations "$ITERATIONS" \
      --timeout "$READY_TIMEOUT" \
      --state-dir "$STATE_DIR" | tee "$READY_JSON" >/dev/null
  done
done

e2e_step "perf footprint (persistent workspace, image=$IMAGE)"
"$CLI" --json create "$WORKSPACE" \
  --image "$IMAGE" \
  --profile tiny \
  --network "$NETWORK" \
  --state-dir "$STATE_DIR" >/dev/null
"$CLI" --json start "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null
FOOTPRINT_JSON="$STATE_DIR/footprint.json"
"$CLI" --json perf footprint "$WORKSPACE" --state-dir "$STATE_DIR" | tee "$FOOTPRINT_JSON" >/dev/null
"$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
"$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" >/dev/null 2>&1 || true

e2e_step "summary"

MICROAGENT_VERSION="$("$CLI" --json version 2>/dev/null | sed -n 's/.*"version"[: ]*"\([^"]*\)".*/\1/p')"
SOURCE_COMMIT="$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || echo unknown)"
SOURCE_STATUS="clean"
if [ -n "$(git -C "$ROOT" status --porcelain --untracked-files=all 2>/dev/null)" ]; then
  SOURCE_STATUS="dirty"
fi
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

python3 - "$BOOT_JSON" "$FOOTPRINT_JSON" "$STATE_DIR" \
  "$CPU_MODEL" "$ARCH" "$RAM_TOTAL" "$OS_NAME" "$KERNEL_RELEASE" "$WSL_NOTE" \
  "$MICROAGENT_VERSION" "$SOURCE_COMMIT" "$SOURCE_STATUS" "$NETWORK" \
  "$CLI" "$CLI_SOURCE" <<'PY'
import hashlib
import json
import pathlib
import sys

(boot_path, footprint_path, state_dir, cpu_model, arch, ram_total, os_name,
 kernel_release, wsl_note, microagent_version, source_commit,
 source_status, network, cli_path, cli_source) = sys.argv[1:]

with open(boot_path) as f:
    boot = json.load(f)
with open(footprint_path) as f:
    footprint = json.load(f)

ready_host = {}
first_ready_path = pathlib.Path(state_dir) / "ready-cold-exec.json"
if first_ready_path.exists():
    with first_ready_path.open() as f:
        ready_host = (json.load(f).get("host") or {})

cli = pathlib.Path(cli_path).resolve()
if cli_source == "checkout":
    guest_arch = ready_host.get("architecture")
    expected = {
        "guest init": cli.with_name(f"microagent-guestinit-{guest_arch}"),
        "supervisor": cli.with_name(
            "microagent-applevf-supervisor"
            if os_name == "Darwin"
            else "microagent-firecracker-supervisor"
        ),
    }
    actual = {
        "guest init": ready_host.get("guestInitPath"),
        "supervisor": ready_host.get("supervisorPath"),
    }
    for label, expected_path in expected.items():
        actual_path = actual[label]
        if not actual_path or pathlib.Path(actual_path).resolve() != expected_path.resolve():
            raise SystemExit(
                f"source-built perf resolved mismatched {label}: "
                f"got {actual_path or 'missing'}, want {expected_path}"
            )

def component_line(label, path):
    if not path:
        return f"component {label}: unavailable"
    resolved = pathlib.Path(path).resolve()
    try:
        hasher = hashlib.sha256()
        with resolved.open("rb") as component:
            for chunk in iter(lambda: component.read(1024 * 1024), b""):
                hasher.update(chunk)
        digest = hasher.hexdigest()
    except OSError as exc:
        return f"component {label}: path={resolved} sha256=unavailable ({exc})"
    return f"component {label}: path={resolved} sha256={digest}"

s = boot.get("summary", {})
rss_kib = footprint.get("rss_kib")
rss_mib = f"{rss_kib / 1024:.1f} MiB" if isinstance(rss_kib, (int, float)) else "n/a"

print()
print("=== microagent perf snapshot ===")
print(f"host: {cpu_model} | {arch} | RAM {ram_total}")
print(f"os: {os_name} {kernel_release}{wsl_note}")
print(f"microagent: version={microagent_version} cli_source={cli_source}")
print(f"checkout: source_commit={source_commit} source_tree={source_status}")
print(f"backend: {boot.get('backend', 'unknown')}")
print(component_line("cli", cli))
print(component_line("supervisor", ready_host.get("supervisorPath")))
print(component_line("guest_init", ready_host.get("guestInitPath")))
print(component_line("vmm", ready_host.get("binaryPath")))
print(f"image: {boot.get('image_ref', 'unknown')} profile={boot.get('profile', 'tiny')} network={network}")
print()
boot_boundary = boot.get("boundary") or {}
print(f"boot boundary: {boot_boundary.get('start', 'unknown')} -> "
      f"{boot_boundary.get('stop', 'unknown')}; "
      f"excluded={','.join(boot_boundary.get('excluded') or []) or 'none'}; "
      f"cache={boot.get('cache_condition', 'unknown')}")
print(f"boot      iterations={s.get('count')} failures={s.get('failures')} "
      f"rootfs=baseline:{s.get('baselines')}/build:{s.get('builds')} "
      f"min={s.get('min_ms')}ms avg={s.get('avg_ms')}ms max={s.get('max_ms')}ms")
print()
print("full readiness: timer starts before the named lifecycle transition and")
print("stops after a successful command on the named interface; setup and")
print("iteration teardown are excluded; host page cache is uncontrolled")
for start_mode in ("cold", "snapshot-fork", "snapshot-restore", "paused-resume"):
    for probe_mode in ("exec", "interactive"):
        path = pathlib.Path(state_dir) / f"ready-{start_mode}-{probe_mode}.json"
        with path.open() as f:
            ready = json.load(f)
        summary = ready.get("summary", {})
        full = summary.get("full_ready_ms", {})
        lifecycle = summary.get("lifecycle_ms", {})
        interface = summary.get("interface_ready_ms", {})
        probe = summary.get("probe_ms", {})
        setup = ready.get("setup") or {}
        setup_note = ""
        if setup:
            setup_note = f" setup_excluded={setup.get('duration_ms')}ms"
        rootfs_note = ""
        if ready.get("start_mode") == "cold_boot":
            rootfs_note = (
                f" rootfs=baseline:{summary.get('baselines')}"
                f"/build:{summary.get('builds')}"
            )
        print(
            f"{ready.get('start_mode', start_mode):16} "
            f"{ready.get('readiness_probe', probe_mode):17} "
            f"n={summary.get('count')} failures={summary.get('failures')} "
            f"full:p50={full.get('p50_ms')}ms/p95={full.get('p95_ms')}ms "
            f"lifecycle:p50={lifecycle.get('p50_ms')}ms "
            f"interface:p50={interface.get('p50_ms')}ms "
            f"probe:p50={probe.get('p50_ms')}ms{rootfs_note}{setup_note}"
        )
print()
print(f"footprint rss={rss_mib} ({rss_kib} KiB)")
print("=================================")
PY
