#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

SCENARIOS=(
  "contract:scripts/dev/runtime-contract-smoke.sh:all"
  "help-usage:scripts/dev/microagent-e2e-help-usage.sh:all"
  "registry-auth:scripts/dev/microagent-e2e-registry-auth.sh:all"
  "text-output:scripts/dev/microagent-e2e-text-output.sh:all"
  "public-surface:scripts/dev/microagent-e2e-public-surface.sh:all"
  "lifecycle:scripts/dev/microagent-e2e-lifecycle.sh:all"
  "lifecycle-matrix:scripts/dev/microagent-e2e-lifecycle-matrix.sh:manual-linux"
  "networking:scripts/dev/microagent-e2e-networking.sh:linux"
  "mediation:scripts/dev/microagent-e2e-mediation.sh:linux"
  "supervision:scripts/dev/microagent-e2e-supervision.sh:linux"
  "applevf-boot:scripts/dev/applevf-boot-smoke.sh:darwin"
  "applevf-direct-console:scripts/dev/applevf-direct-console-smoke.sh:darwin"
  "applevf-substrate:scripts/dev/applevf-substrate-smoke.sh:darwin"
  "applevf-workspace-connect:scripts/dev/applevf-workspace-connect-smoke.sh:darwin"
  "applevf-network-mode:scripts/dev/applevf-network-mode-smoke.sh:darwin"
  "applevf-publish:scripts/dev/applevf-publish-smoke.sh:darwin"
  "applevf-vsock-diagnostic:scripts/dev/applevf-vsock-diagnostic-smoke.sh:darwin"
)

usage() {
  cat <<'EOF'
microagent-e2e.sh

Runs the full microagent end-to-end suite for the current host backend.

Usage:
  scripts/dev/microagent-e2e.sh [--keep] [--image-cache-policy auto|refresh|require] [scenario...]
  scripts/dev/microagent-e2e.sh --list

Scenarios:
  contract          Runtime contract JSON and synthetic state/result/artifact
                    compatibility checks
  help-usage        CLI help output and invalid invocation usage errors
  registry-auth     Standard registry credential discovery against a
                    local private OCI registry
  text-output       Human/text output mode for stable public CLI surfaces
  public-surface     CLI contract, host/doctor, kernel/rootfs, run/result,
                     request JSON, bundles, attached-disk artifacts, kill, perf
  lifecycle          Backend-agnostic lifecycle contract scenario. Defaults to
                     Firecracker on Linux and Apple VF on macOS; override with
                     MICROAGENT_E2E_BACKEND=firecracker|applevf
  lifecycle-matrix   create/start/status/ps/connect/logs/halt/resume/cp/clone,
                     validation failures, images, artifacts, quarantine/delete
  networking         user/nat/bridged networking, published ports, outbound
                     connectivity, event history, resume behavior
  mediation          required mediation channel and quarantine fail-closed behavior
  supervision        restart policy behavior for never/always
  applevf-boot       Apple VF run boot smoke for a BusyBox workload
  applevf-direct-console
                    Apple VF direct supervisor console input smoke
  applevf-substrate  Apple VF create/start/halt/resume/quarantine/artifact smoke
  applevf-workspace-connect
                    Apple VF workspace connect/logs/ps smoke
  applevf-network-mode
                    Apple VF user/nat/isolated/bridged check and outbound smoke
  applevf-publish    Apple VF TCP publish forwarding smoke
  applevf-vsock-diagnostic
                    Apple VF mediation and virtio-vsock diagnostic smoke

Environment:
  --keep or MICROAGENT_E2E_KEEP=1 keeps failed and successful scenario state directories.
  MICROAGENT_E2E_IMAGE=<ref> overrides the default BusyBox public-surface image.
  MICROAGENT_NATS_IMAGE=<ref> overrides the default NATS image used by scenarios.
  MICROAGENT_E2E_CACHE_DIR=<dir> overrides the shared Go build/module cache.
  MICROAGENT_E2E_IMAGE_CACHE_DIR=<dir> overrides the persistent E2E image cache.
  MICROAGENT_ROOTFS_BASE_CACHE_DIR=<dir> overrides the persistent rootfs base cache.
  MICROAGENT_E2E_IMAGE_CACHE_POLICY=auto|refresh|require controls persistent
    E2E image cache use for scenarios that support it.
  MICROAGENT_E2E_REFRESH_IMAGE_CACHE=1 refreshes cached E2E image rootfs files
    for compatibility with older validation commands.
  MICROAGENT_E2E_BACKEND=firecracker|applevf selects the backend lane for
    backend-agnostic feature scenarios.
  MICROAGENT_FIRECRACKER_SUPERVISOR=<path> uses a prepared supervisor binary.
  MICROAGENT_E2E_BRIDGE=<name> uses a prepared Linux bridge for bridged tests.
  MICROAGENT_APPLEVF_SUPERVISOR=<path> uses a prepared Apple VF supervisor binary.
  MICROAGENT_APPLEVF_KERNEL=<path> uses a prepared Apple VF Linux ARM64 kernel.

Linux nat/bridged setup:
  scripts/dev/microagent-e2e-linux-network-setup.sh

Apple VF setup:
  scripts/dev/applevf-supervisor-build.sh
EOF
}

scenario_script() {
  wanted="$1"
  for entry in "${SCENARIOS[@]}"; do
    name="${entry%%:*}"
    rest="${entry#*:}"
    script="${rest%%:*}"
    if [ "$name" = "$wanted" ]; then
      printf '%s\n' "$script"
      return 0
    fi
  done
  return 1
}

scenario_platform() {
  wanted="$1"
  for entry in "${SCENARIOS[@]}"; do
    name="${entry%%:*}"
    rest="${entry#*:}"
    platform="${rest##*:}"
    if [ "$name" = "$wanted" ]; then
      printf '%s\n' "$platform"
      return 0
    fi
  done
  return 1
}

scenario_supported() {
  platform="$(scenario_platform "$1")"
  case "$platform" in
    all)
      return 0
      ;;
    linux)
      [ "$(uname -s)" = "Linux" ]
      ;;
    darwin)
      [ "$(uname -s)" = "Darwin" ]
      ;;
    *)
      return 1
      ;;
  esac
}

list_scenarios() {
  for entry in "${SCENARIOS[@]}"; do
    name="${entry%%:*}"
    platform="${entry##*:}"
    printf '%-18s %s\n' "$name" "$platform"
  done
}

keep="${MICROAGENT_E2E_KEEP:-0}"
args=()
while [ "$#" -gt 0 ]; do
  case "$1" in
    --help|-h)
      usage
      exit 0
      ;;
    --list)
      list_scenarios
      exit 0
      ;;
    --keep)
      keep=1
      shift
      ;;
    --image-cache-policy)
      if [ "$#" -lt 2 ]; then
        echo "--image-cache-policy requires auto, refresh, or require" >&2
        exit 2
      fi
      export MICROAGENT_E2E_IMAGE_CACHE_POLICY="$2"
      shift 2
      ;;
    --image-cache-policy=*)
      export MICROAGENT_E2E_IMAGE_CACHE_POLICY="${1#*=}"
      shift
      ;;
    --)
      shift
      while [ "$#" -gt 0 ]; do
        args+=("$1")
        shift
      done
      ;;
    --*)
      echo "unknown microagent E2E option: $1" >&2
      usage >&2
      exit 2
      ;;
    *)
      args+=("$1")
      shift
      ;;
  esac
done

if [ -n "${MICROAGENT_E2E_IMAGE_CACHE_POLICY:-}" ]; then
  case "$MICROAGENT_E2E_IMAGE_CACHE_POLICY" in
    auto|refresh|require)
      ;;
    *)
      echo "unknown image cache policy: $MICROAGENT_E2E_IMAGE_CACHE_POLICY" >&2
      exit 2
      ;;
  esac
fi

if [ "$keep" = "1" ]; then
  export MICROAGENT_KEEP_MICROAGENT_E2E_HELP_USAGE=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_REGISTRY_AUTH=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_TEXT_OUTPUT=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_PUBLIC_SURFACE=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_LIFECYCLE=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_LIFECYCLE_MATRIX=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_NETWORKING=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_MEDIATION=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_SUPERVISION=1
  export MICROAGENT_KEEP_BOOT_SMOKE=1
  export MICROAGENT_KEEP_DIRECT_CONSOLE_SMOKE=1
  export MICROAGENT_KEEP_APPLEVF_SUBSTRATE_SMOKE=1
  export MICROAGENT_KEEP_CONNECT_SMOKE=1
  export MICROAGENT_KEEP_NETWORK_SMOKE=1
  export MICROAGENT_KEEP_APPLEVF_PUBLISH_SMOKE=1
  export MICROAGENT_KEEP_APPLEVF_MEDIATION_SMOKE=1
fi

selected=()
if [ "${#args[@]}" -eq 0 ]; then
  for entry in "${SCENARIOS[@]}"; do
    name="${entry%%:*}"
    if scenario_supported "$name"; then
      selected+=("$name")
    fi
  done
else
  for name in "${args[@]}"; do
    if ! scenario_script "$name" >/dev/null; then
      echo "unknown microagent E2E scenario: $name" >&2
      echo "available scenarios:" >&2
      list_scenarios >&2
      exit 2
    fi
    selected+=("$name")
  done
fi

printf 'microagent E2E suite: %s\n' "${selected[*]}"
start_suite="$(date +%s)"
suite_cache_dir="${MICROAGENT_E2E_CACHE_DIR:-$ROOT/.cache/microagent-e2e}"
export GOCACHE="${GOCACHE:-$suite_cache_dir/go-build}"
export GOMODCACHE="${GOMODCACHE:-$suite_cache_dir/gomodcache}"
export GOFLAGS="${GOFLAGS:-} -modcacherw"
mkdir -p "$GOCACHE" "$GOMODCACHE"

for name in "${selected[@]}"; do
  script="$(scenario_script "$name")"
  printf '\n==> %s\n' "$name"
  start="$(date +%s)"
  (
    cd "$ROOT"
    "$script"
  )
  end="$(date +%s)"
  printf '<== %s passed in %ss\n' "$name" "$((end - start))"
done

end_suite="$(date +%s)"
printf '\nmicroagent E2E suite passed in %ss\n' "$((end_suite - start_suite))"
