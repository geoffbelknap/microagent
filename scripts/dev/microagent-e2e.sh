#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

# Each entry: name:script:platform:requirement
#   platform    = all | linux | darwin (selected only on a matching host)
#   requirement = none | vm | netpriv
#     none    - always runnable (no microVM boot needed)
#     vm      - needs a microVM backend (skip-with-reason when absent)
#     netpriv - needs privileged Linux networking (root/CAP_NET_ADMIN + ip_forward)
SCENARIOS=(
  "coverage-matrix:scripts/dev/microagent-e2e-coverage-matrix.sh:all:none"
  "contract:scripts/dev/runtime-contract-smoke.sh:all:none"
  "help-usage:scripts/dev/microagent-e2e-help-usage.sh:all:none"
  "mcp-stdio:scripts/dev/microagent-e2e-mcp.sh:all:none"
  "registry-auth:scripts/dev/microagent-e2e-registry-auth.sh:all:none"
  "text-output:scripts/dev/microagent-e2e-text-output.sh:all:none"
  "init:scripts/dev/microagent-e2e-init.sh:all:none"
  "mcp-lifecycle:scripts/dev/microagent-e2e-mcp-lifecycle.sh:all:vm"
  "survive-reboot:scripts/dev/microagent-e2e-survive-reboot.sh:all:vm"
  "public-surface:scripts/dev/microagent-e2e-public-surface.sh:all:vm"
  "lifecycle-deep:scripts/dev/microagent-e2e-lifecycle.sh:all:vm"
  "networking-deep:scripts/dev/microagent-e2e-networking-contract.sh:all:vm"
  "transport-deep:scripts/dev/microagent-e2e-transport.sh:all:vm"
  "supervision-deep:scripts/dev/microagent-e2e-supervision-contract.sh:all:vm"
  "volumes:scripts/dev/microagent-e2e-volumes.sh:all:vm"
  "commit-images:scripts/dev/microagent-e2e-commit.sh:all:vm"
  "secrets:scripts/dev/microagent-e2e-secrets.sh:all:vm"
  "health:scripts/dev/microagent-e2e-health.sh:all:vm"
  "exec-stream:scripts/dev/microagent-e2e-exec-stream.sh:all:vm"
  "model-serving:scripts/dev/microagent-e2e-model.sh:all:vm"
  "host-worker-gpu:scripts/dev/microagent-e2e-host-worker-gpu.sh:linux:vm"
  "firecracker-lifecycle-host:scripts/dev/microagent-e2e-lifecycle-matrix.sh:linux:vm"
  "firecracker-networking-host:scripts/dev/microagent-e2e-networking.sh:linux:vm"
  "firecracker-transport-host:scripts/dev/microagent-e2e-mediation.sh:linux:vm"
  "firecracker-supervision-host:scripts/dev/microagent-e2e-supervision.sh:linux:vm"
  "named-network:scripts/dev/microagent-e2e-named-network.sh:linux:netpriv"
  "windows-hyperv-lifecycle-host:scripts/dev/microagent-e2e-windows-hyperv-lifecycle-host.sh:windows:vm"
  "windows-hyperv-connect-host:scripts/dev/microagent-e2e-windows-hyperv-connect-host.sh:windows:vm"
  "windows-hyperv-exec-host:scripts/dev/microagent-e2e-windows-hyperv-exec-host.sh:windows:vm"
  "windows-hyperv-transport-host:scripts/dev/microagent-e2e-windows-hyperv-transport-host.sh:windows:vm"
  "windows-hyperv-model-host:scripts/dev/microagent-e2e-windows-hyperv-model-host.sh:windows:vm"
  "windows-hyperv-named-network-host:scripts/dev/microagent-e2e-windows-hyperv-named-network-host.sh:windows:vm"
  "applevf-boot:scripts/dev/applevf-boot-smoke.sh:darwin:vm"
  "applevf-direct-console:scripts/dev/applevf-direct-console-smoke.sh:darwin:vm"
  "applevf-substrate:scripts/dev/applevf-substrate-smoke.sh:darwin:vm"
  "applevf-workspace-connect:scripts/dev/applevf-workspace-connect-smoke.sh:darwin:vm"
  "applevf-network-mode:scripts/dev/applevf-network-mode-smoke.sh:darwin:vm"
  "applevf-publish:scripts/dev/applevf-publish-smoke.sh:darwin:vm"
  "applevf-vsock-diagnostic:scripts/dev/applevf-vsock-diagnostic-smoke.sh:darwin:vm"
)

# Each entry: scenario|coverage|backends|feature summary.
#   coverage = portable | backend-neutral | backend-specific | host-specific
#   backends = none | host-default | firecracker,apple-vf | firecracker | apple-vf
SCENARIO_COVERAGE=(
  "coverage-matrix|portable|none|E2E feature inventory and scenario metadata"
  "contract|portable|none|runtime contract, synthetic state/result/artifacts"
  "help-usage|portable|none|help, usage errors, unsupported container-style flags"
  "mcp-stdio|portable|none|serve mcp, initialize, tools/list, ping, describe"
  "mcp-lifecycle|backend-neutral|firecracker,apple-vf,windows-hyperv|serve mcp workspace create/start/exec/halt/delete with CLI parity"
  "registry-auth|portable|none|registry credentials and private OCI pull auth"
  "text-output|portable|none|human output mode for stable public CLI surfaces"
  "init|portable|none|init scaffold, providers, --force, generated spec validation"
  "survive-reboot|host-specific|host-default|supervise --install/--uninstall boot units; no real reboot"
  "public-surface|backend-neutral|firecracker,apple-vf,windows-hyperv|version, contract, profiles, host, doctor, kernel, rootfs, run/result, artifact, perf, image prune"
  "lifecycle-deep|backend-neutral|firecracker,apple-vf,windows-hyperv|create/start/status/list/connect/logs/events/stats/halt/quarantine/clone/cp/artifact/image/delete"
  "networking-deep|backend-neutral|firecracker,apple-vf,windows-hyperv|network modes, publish, apply, quarantine, cached image/network paths"
  "transport-deep|backend-neutral|firecracker,apple-vf,windows-hyperv|mediation and vsock transport contract"
  "supervision-deep|backend-neutral|firecracker,apple-vf,windows-hyperv|restart supervision, signal, failure, cleanup"
  "volumes|backend-neutral|firecracker,apple-vf,windows-hyperv|volume create/list/status/delete, attach persistence, single attach"
  "commit-images|backend-neutral|firecracker,apple-vf,windows-hyperv|commit stopped rootfs into local OCI image layout"
  "secrets|backend-neutral|firecracker,apple-vf,windows-hyperv|secret check, materialized secrets, on-demand secrets, audit records"
  "health|backend-neutral|firecracker,apple-vf,windows-hyperv|health.exec validation and supervise restart on unhealthy probe"
  "exec-stream|backend-neutral|firecracker,apple-vf,windows-hyperv|structured exec streaming, non-zero exit propagation, buffered parity"
  "model-serving|backend-neutral|firecracker,apple-vf,windows-hyperv|model pull/list/stop and run --model over backend vsock bridge"
  "host-worker-gpu|host-specific|firecracker|Opt-in host GPU worker acceptance over the external runner bridge"
  "firecracker-lifecycle-host|backend-specific|firecracker|Firecracker lifecycle host mechanics"
  "firecracker-networking-host|backend-specific|firecracker|Firecracker TAP, bridge, NAT, helper mechanics"
  "firecracker-transport-host|backend-specific|firecracker|Firecracker /dev/vhost-vsock and helper mechanics"
  "firecracker-supervision-host|backend-specific|firecracker|Firecracker helper PID cleanup mechanics"
  "named-network|host-specific|firecracker|Linux privileged named-network bridge and DNS"
  "windows-hyperv-lifecycle-host|backend-specific|windows-hyperv|Hyper-V boot, structured result delivery over hv_sock"
  "windows-hyperv-connect-host|backend-specific|windows-hyperv|Hyper-V socket shell connect and readiness"
  "windows-hyperv-exec-host|backend-specific|windows-hyperv|Structured exec bridge: buffered, stream, exit codes, readiness"
  "windows-hyperv-transport-host|backend-specific|windows-hyperv|Mediation channel over Hyper-V socket listener helpers"
  "windows-hyperv-model-host|backend-specific|windows-hyperv|run/start --model pairing and the guest model URL bridge with a stand-in engine (no llama.cpp)"
  "windows-hyperv-named-network-host|host-specific|windows-hyperv|Private named network: two workspaces join, get stable 10.44.x IPs on a shared HNS network, reach each other by IP"
  "applevf-boot|backend-specific|apple-vf|Apple VF boot smoke"
  "applevf-direct-console|backend-specific|apple-vf|Apple VF direct supervisor console input"
  "applevf-substrate|backend-specific|apple-vf|Apple VF lifecycle substrate smoke"
  "applevf-workspace-connect|backend-specific|apple-vf|Apple VF connect/logs/ps smoke"
  "applevf-network-mode|backend-specific|apple-vf|Apple VF user/nat/isolated/bridged network modes"
  "applevf-publish|backend-specific|apple-vf|Apple VF TCP publish forwarding"
  "applevf-vsock-diagnostic|backend-specific|apple-vf|Apple VF mediation and virtio-vsock diagnostics"
)

# Each entry: feature|classification|required backends|covering scenarios|notes.
# The matrix is intentionally user-facing: it maps CLI/workspace surfaces to
# practical E2E coverage and makes unsupported/backend-specific areas explicit.
E2E_MATRIX=(
  "help/version|portable|none|help-usage,text-output|No VM required"
  "init|portable|none|init|Scaffold and generated spec validation"
  "contract|portable|none|contract,public-surface|Runtime contract and synthetic compatibility checks"
  "profiles|portable|none|public-surface,lifecycle-deep|Profile list and manifest use"
  "host/doctor|portable|host-default|public-surface,lifecycle-deep|Host capability and diagnostics for selected backend"
  "kernel install/verify|portable|host-default|public-surface,health,volumes|Backend-specific artifacts through a common CLI"
  "rootfs build|portable|host-default|public-surface,lifecycle-deep|OCI rootfs build plus validation failures"
  "run/create/start|backend-neutral|firecracker,apple-vf,windows-hyperv|public-surface,lifecycle-deep,health,volumes,model-serving,windows-hyperv-lifecycle-host|Core workspace boot paths"
  "status/list|backend-neutral|firecracker,apple-vf,windows-hyperv|public-surface,lifecycle-deep,applevf-workspace-connect|State/readiness/list surfaces"
  "result/logs/artifact|backend-neutral|firecracker,apple-vf,windows-hyperv|contract,public-surface,lifecycle-deep|Structured result, serial logs, declared artifacts"
  "events/stats|backend-neutral|firecracker,apple-vf,windows-hyperv|lifecycle-deep|Lifecycle event history and resource sampling"
  "connect|backend-neutral|firecracker,apple-vf,windows-hyperv|lifecycle-deep,applevf-workspace-connect,windows-hyperv-connect-host|Interactive and send-mode console paths"
  "exec|backend-neutral|firecracker,apple-vf,windows-hyperv|health,exec-stream,secrets,volumes,windows-hyperv-exec-host|Structured exec and streaming exec"
  "halt/quarantine/stop/kill/delete|backend-neutral|firecracker,apple-vf,windows-hyperv|public-surface,lifecycle-deep,supervision-deep|Lifecycle controls and cleanup"
  "clone/cp|backend-neutral|firecracker,apple-vf,windows-hyperv|lifecycle-deep|Stopped workspace copy and clone semantics; windows-hyperv cp rides a guest maintenance boot over exec"
  "apply|backend-neutral|firecracker,apple-vf,windows-hyperv|networking-deep|Supported spec changes"
  "network status/modes/publish|backend-neutral|firecracker,apple-vf,windows-hyperv|networking-deep,applevf-network-mode,applevf-publish|Portable modes plus backend publish mechanics; windows-hyperv HNS segments need an elevated host"
  "network create/list/delete named|host-specific|firecracker,windows-hyperv|named-network,windows-hyperv-named-network-host|Privileged Linux named bridge and elevated windows-hyperv private HNS network; not Apple VF portable"
  "volume create/list/status/delete|backend-neutral|firecracker,apple-vf,windows-hyperv|volumes|Managed volume lifecycle and attach semantics (ext4, or VHD-wrapped ext4 on windows-hyperv)"
  "commit/image|backend-neutral|firecracker,apple-vf,windows-hyperv|commit-images,lifecycle-deep,public-surface|Local OCI image records, tag/delete/prune, commit"
  "registry auth|portable|none|registry-auth|Private registry credential discovery"
  "secrets|backend-neutral|firecracker,apple-vf,windows-hyperv|secrets|Secret reference validation, materialized/on-demand delivery, audit"
  "health|backend-neutral|firecracker,apple-vf,windows-hyperv|health|Exec probes and supervise restart"
  "supervise|backend-neutral|firecracker,apple-vf,windows-hyperv|supervision-deep,health,survive-reboot|Restart loop plus host boot-unit generation"
  "snapshot/pause/resume|backend-specific|firecracker,windows-hyperv|firecracker-lifecycle-host,lifecycle-deep|vCPU pause/resume is implemented on Firecracker and windows-hyperv (HCS pause/resume, exercised by lifecycle-deep); memory snapshot is still Firecracker-only, planned on apple-vf"
  "model|backend-neutral|firecracker,apple-vf,windows-hyperv|model-serving|Model store and run --model vsock pairing"
  "host worker GPU|host-specific|firecracker|host-worker-gpu|Opt-in acceptance matrix for a host GPU worker reached by one and two Firecracker microVMs"
  "perf|backend-neutral|firecracker,apple-vf,windows-hyperv|public-surface|Boot/steady/footprint surfaces where host supports sampling; windows-hyperv samples HCS statistics"
  "serve mcp|portable|none|mcp-stdio|MCP stdio transport and capability manifest"
  "serve mcp lifecycle|backend-neutral|firecracker,apple-vf,windows-hyperv|mcp-lifecycle|Workspace lifecycle driven through MCP tools with CLI parity"
  "AX/text output|portable|none|text-output,mcp-stdio|Structured AX and human text output contracts"
)

usage() {
  cat <<'EOF'
microagent-e2e.sh

Runs the full microagent end-to-end suite for the current host backend.

Usage:
  scripts/dev/microagent-e2e.sh [--keep] [--image-cache-policy auto|refresh|require] [scenario...]
  scripts/dev/microagent-e2e.sh --list
  scripts/dev/microagent-e2e.sh --matrix

Scenarios:
  coverage-matrix  Verifies the E2E feature matrix covers user-facing CLI and
                    workspace surfaces and references known scenarios.
  contract          Runtime contract JSON and synthetic state/result/artifact
                    compatibility checks
  help-usage        CLI help output and invalid invocation usage errors
  mcp-stdio         MCP stdio initialize/tools/list/tool-call smoke
  mcp-lifecycle     Workspace create/start/exec/halt/delete driven through MCP
                    tool calls against a real microVM, with CLI parity checks
  registry-auth     Standard registry credential discovery against a
                    local private OCI registry
  text-output       Human/text output mode for stable public CLI surfaces
  public-surface     CLI contract, host/doctor, kernel/rootfs, run/result,
                     request JSON, bundles, attached-disk artifacts, kill, perf
  lifecycle-deep     Backend-neutral lifecycle feature contract:
                     create/start/status/list/connect/logs/halt/resume/cp/clone,
                     validation failures, image, artifact, quarantine/delete.
                     Defaults to Firecracker on Linux, Apple VF on macOS, and
                     windows-hyperv on Windows; override with
                     MICROAGENT_E2E_BACKEND=firecracker|applevf|windows-hyperv.
  networking-deep    Backend-neutral networking feature contract. Covers modes,
                     publish, cached NATS/rootfs, apply, artifacts,
                     halt/resume, quarantine, and invalid config paths where
                     each backend has matching semantics.
  transport-deep     Backend-neutral mediation/vsock transport feature contract.
  supervision-deep   Backend-neutral restart supervision, signal, failure, and
                     cleanup feature contract.
  init               Agent scaffold (no VM): generated files, providers,
                     --force, and that the generated spec validates.
  volumes            Named-volume registry, ext4 backing, attach-by-name
                     persistence across runs, and single-attach enforcement.
  commit-images      Commit a stopped rootfs into the local OCI image layout;
                     refuses a running workspace.
  secrets            Materialized and on-demand secret delivery over vsock,
                     with host audit records that do not leak values.
  health             Health-probe config contract (valid boots, invalid is
                     rejected) and probe success in the guest.
  exec-stream        Streaming structured exec (exec --stream) line delivery and
                     exit-status propagation.
  model-serving      Local host model server paired into a workspace over the
                     backend vsock bridge (Firecracker on Linux, Apple VF on
                     macOS, Hyper-V sockets on Windows).
  host-worker-gpu    Opt-in Linux/Firecracker host GPU worker acceptance
                     matrix. Set MICROAGENT_E2E_HOST_WORKER_GPU=1 and either
                     MICROAGENT_LLAMA_SERVER or MICROAGENT_E2E_HOST_WORKER_URL.
  survive-reboot     supervise --install/--uninstall boot-unit generation
                     (systemd user unit / launchd plist); no real reboot.
  named-network      Two workspaces on a managed named-network bridge: stable
                     IPs, cross-VM reach by IP and by name. Privileged (netpriv).
  firecracker-lifecycle-host
                    Firecracker/Linux host mechanics probe behind lifecycle.
  firecracker-networking-host
                    Firecracker/Linux TAP, bridge, NAT, and helper mechanics.
  firecracker-transport-host
                    Firecracker/Linux /dev/vhost-vsock and helper mechanics.
  firecracker-supervision-host
                    Firecracker/Linux helper PID cleanup mechanics.
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
  DOCKER_CONFIG=<dir> overrides registry credential discovery for image pulls.
    When unset, the suite uses an empty test-local Docker config for public
    image scenarios so host credential-helper state cannot break E2E pulls.
    The registry-auth scenario preserves the original DOCKER_CONFIG state.
  MICROAGENT_E2E_IMAGE_CACHE_POLICY=auto|refresh|require controls persistent
    E2E image cache use for scenarios that support it.
  MICROAGENT_E2E_REFRESH_IMAGE_CACHE=1 refreshes cached E2E image rootfs files
    for compatibility with older validation commands.
  MICROAGENT_E2E_BACKEND=firecracker|applevf|windows-hyperv selects the backend
    lane for backend-agnostic feature scenarios. Windows runs use Git Bash with
    the windows-hyperv backend (Hyper-V role + HCS services).
  MICROAGENT_E2E_HOST_WORKER_GPU=1 opts into the Linux/Firecracker host GPU
    worker acceptance scenario. It is skipped by default so regular E2E runs do
    not require a GPU or local model runner.
  MICROAGENT_E2E_HOST_WORKER_URL=<url> uses an existing OpenAI-compatible host
    worker for host-worker-gpu; otherwise set MICROAGENT_LLAMA_SERVER.
  MICROAGENT_E2E_HOST_WORKER_OUT_DIR=<dir> stores host-worker-gpu reports.
  MICROAGENT_E2E_ALLOW_NETPRIV=1 opts the privileged networking lane in when you
    hold CAP_NET_ADMIN without uid 0 (file caps / capability-granting sandbox).
  MICROAGENT_E2E_HEARTBEAT=<seconds> sets the "still running" heartbeat interval
    for long scenarios (default 20; scenarios faster than this stay quiet).

Scenarios that need a microVM backend (or privileged networking) skip with a
reason when the host lacks the prerequisite; a preflight line and a final
PASSED/SKIPPED/FAILED summary report what was validated.
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

scenario_meta() {
  wanted="$(canonical_scenario "$1")"
  for entry in "${SCENARIO_COVERAGE[@]}"; do
    name="${entry%%|*}"
    if [ "$name" = "$wanted" ]; then
      rest="${entry#*|}"
      coverage="${rest%%|*}"
      rest="${rest#*|}"
      backends="${rest%%|*}"
      features="${rest#*|}"
      case "$2" in
        coverage) printf '%s\n' "$coverage" ;;
        backends) printf '%s\n' "$backends" ;;
        features) printf '%s\n' "$features" ;;
      esac
      return 0
    fi
  done
  case "$2" in
    coverage) printf '%s\n' unknown ;;
    backends) printf '%s\n' unknown ;;
    features) printf '%s\n' "missing scenario metadata" ;;
  esac
}

# scenario_field <name> <index>: print field 2=script, 3=platform, 4=requirement.
scenario_field() {
  wanted="$(canonical_scenario "$1")"
  for entry in "${SCENARIOS[@]}"; do
    f_name="${entry%%:*}"
    if [ "$f_name" = "$wanted" ]; then
      rest="${entry#*:}"
      f_script="${rest%%:*}"
      rest="${rest#*:}"
      f_platform="${rest%%:*}"
      f_req="${rest#*:}"
      case "$2" in
        2) printf '%s\n' "$f_script" ;;
        3) printf '%s\n' "$f_platform" ;;
        4) printf '%s\n' "${f_req:-vm}" ;;
      esac
      return 0
    fi
  done
  return 1
}

scenario_script() { scenario_field "$1" 2; }
scenario_platform() { scenario_field "$1" 3; }
scenario_requirement() { scenario_field "$1" 4; }

canonical_scenario() {
  case "$1" in
    lifecycle|lifecycle-matrix)
      printf '%s\n' lifecycle-deep
      ;;
    networking|networking-linux)
      printf '%s\n' networking-deep
      ;;
    transport|mediation|mediation-linux)
      printf '%s\n' transport-deep
      ;;
    supervision|supervision-linux)
      printf '%s\n' supervision-deep
      ;;
    *)
      printf '%s\n' "$1"
      ;;
  esac
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
    windows)
      e2e_is_windows
      ;;
    *)
      return 1
      ;;
  esac
}

list_scenarios() {
  if [ "${MICROAGENT_E2E_LIST_TSV:-0}" = "1" ]; then
    printf 'SCENARIO\tPLATFORM\tREQUIRES\tCOVERAGE\tBACKENDS\tFEATURES\n'
    for entry in "${SCENARIOS[@]}"; do
      name="${entry%%:*}"
      rest="${entry#*:}"; rest="${rest#*:}"
      platform="${rest%%:*}"
      req="${rest#*:}"
      printf '%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$name" "$platform" "${req:-vm}" \
        "$(scenario_meta "$name" coverage)" \
        "$(scenario_meta "$name" backends)" \
        "$(scenario_meta "$name" features)"
    done
    return 0
  fi
  printf '%-28s %-8s %-8s %-16s %-20s %s\n' "SCENARIO" "PLATFORM" "REQUIRES" "COVERAGE" "BACKENDS" "FEATURES"
  for entry in "${SCENARIOS[@]}"; do
    name="${entry%%:*}"
    rest="${entry#*:}"; rest="${rest#*:}"
    platform="${rest%%:*}"
    req="${rest#*:}"
    printf '%-28s %-8s %-8s %-16s %-20s %s\n' \
      "$name" "$platform" "${req:-vm}" \
      "$(scenario_meta "$name" coverage)" \
      "$(scenario_meta "$name" backends)" \
      "$(scenario_meta "$name" features)"
  done
}

list_matrix() {
  if [ "${MICROAGENT_E2E_MATRIX_TSV:-0}" = "1" ]; then
    printf 'FEATURE\tCLASS\tREQUIRED_BACKENDS\tSCENARIOS\tNOTES\n'
    for entry in "${E2E_MATRIX[@]}"; do
      feature="${entry%%|*}"
      rest="${entry#*|}"
      class="${rest%%|*}"
      rest="${rest#*|}"
      backends="${rest%%|*}"
      rest="${rest#*|}"
      scenarios="${rest%%|*}"
      notes="${rest#*|}"
      printf '%s\t%s\t%s\t%s\t%s\n' "$feature" "$class" "$backends" "$scenarios" "$notes"
    done
    return 0
  fi
  printf '%-30s %-18s %-22s %-44s %s\n' "FEATURE" "CLASS" "REQUIRED_BACKENDS" "SCENARIOS" "NOTES"
  for entry in "${E2E_MATRIX[@]}"; do
    feature="${entry%%|*}"
    rest="${entry#*|}"
    class="${rest%%|*}"
    rest="${rest#*|}"
    backends="${rest%%|*}"
    rest="${rest#*|}"
    scenarios="${rest%%|*}"
    notes="${rest#*|}"
    printf '%-30s %-18s %-22s %-44s %s\n' "$feature" "$class" "$backends" "$scenarios" "$notes"
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
    --matrix)
      list_matrix
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
  export MICROAGENT_KEEP_MICROAGENT_E2E_COVERAGE_MATRIX=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_HELP_USAGE=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_MCP=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_REGISTRY_AUTH=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_TEXT_OUTPUT=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_PUBLIC_SURFACE=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_LIFECYCLE=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_LIFECYCLE_MATRIX=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_NETWORKING=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_MEDIATION=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_SUPERVISION=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_SURVIVE_REBOOT=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_VOLUMES=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_COMMIT=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_SECRETS=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_HEALTH=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_EXEC_STREAM=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_HOST_WORKER_GPU=1
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
    resolved="$(canonical_scenario "$name")"
    if ! scenario_script "$resolved" >/dev/null; then
      echo "unknown microagent E2E scenario: $name" >&2
      echo "available scenarios:" >&2
      list_scenarios >&2
      exit 2
    fi
    selected+=("$resolved")
  done
fi

# Preflight: probe host capabilities once so the summary explains skips.
have_vm=no; e2e_have_vm && have_vm=yes
have_netpriv=no; e2e_have_netpriv && have_netpriv=yes
is_wsl=no; e2e_is_wsl && is_wsl=yes
printf 'microagent E2E preflight: os=%s arch=%s wsl=%s vm=%s netpriv=%s\n' \
  "$(e2e_friendly_os)" "$(uname -m)" "$is_wsl" "$have_vm" "$have_netpriv"
if [ "$have_vm" = "no" ]; then
  printf '  (no microVM backend: vm/netpriv scenarios will SKIP. Run microagent doctor for details.)\n'
fi
printf 'microagent E2E suite: %s\n' "${selected[*]}"

start_suite="$(date +%s)"
suite_cache_dir="${MICROAGENT_E2E_CACHE_DIR:-$ROOT/.cache/microagent-e2e}"
export GOCACHE="${GOCACHE:-$suite_cache_dir/go-build}"
export GOMODCACHE="${GOMODCACHE:-$suite_cache_dir/gomodcache}"
export GOFLAGS="${GOFLAGS:-} -modcacherw"
mkdir -p "$GOCACHE" "$GOMODCACHE"

original_docker_config_set=0
original_docker_config=""
if [ "${DOCKER_CONFIG+x}" = "x" ]; then
  original_docker_config_set=1
  original_docker_config="$DOCKER_CONFIG"
else
  export DOCKER_CONFIG="$suite_cache_dir/docker-config-empty"
  mkdir -p "$DOCKER_CONFIG"
fi

passed=(); skipped=(); failed=()
for name in "${selected[@]}"; do
  script="$(scenario_script "$name")"
  requirement="$(scenario_requirement "$name")"

  # Requirement gating: skip cleanly (with a reason) when the host can't run it.
  if [ "$requirement" = "vm" ] && [ "$have_vm" = "no" ]; then
    printf '\n==> %s\n.. SKIP (no microVM backend)\n' "$name"
    skipped+=("$name (no vm)")
    continue
  fi
  if [ "$requirement" = "netpriv" ] && [ "$have_netpriv" = "no" ]; then
    printf '\n==> %s\n.. SKIP (privileged networking not enabled)\n   To enable: microagent host setup-networking, then run as root or set MICROAGENT_E2E_ALLOW_NETPRIV=1\n' "$name"
    skipped+=("$name (no netpriv)")
    continue
  fi

  printf '\n==> %s\n' "$name"
  start="$(date +%s)"
  status=0
  # Run in the background so a heartbeat can show the scenario is alive during
  # long silent stretches (VM boots, perf loops). Scenario output still streams
  # live; the heartbeat only fires once a scenario runs past the interval.
  if [ "$name" = "registry-auth" ] && [ "$original_docker_config_set" = "0" ]; then
    ( cd "$ROOT"; env -u DOCKER_CONFIG "$script" ) &
  elif [ "$name" = "registry-auth" ]; then
    ( cd "$ROOT"; DOCKER_CONFIG="$original_docker_config" "$script" ) &
  else
    ( cd "$ROOT"; "$script" ) &
  fi
  run_pid=$!
  hb_interval="${MICROAGENT_E2E_HEARTBEAT:-20}"
  while sleep "$hb_interval"; do
    kill -0 "$run_pid" 2>/dev/null || break
    printf '   .. %s still running (%ss elapsed)\n' "$name" "$(( $(date +%s) - start ))"
  done
  wait "$run_pid" || status=$?
  end="$(date +%s)"
  if [ "$status" -eq 0 ]; then
    printf '<== %s passed in %ss\n' "$name" "$((end - start))"
    passed+=("$name")
  elif [ "$status" -eq "$E2E_SKIP_EXIT" ]; then
    printf '<== %s skipped\n' "$name"
    skipped+=("$name (self)")
  else
    printf '<== %s FAILED (exit %s) in %ss\n' "$name" "$status" "$((end - start))"
    failed+=("$name")
  fi
done

end_suite="$(date +%s)"
printf '\n==== microagent E2E summary (%ss) ====\n' "$((end_suite - start_suite))"
printf 'PASSED:  %s\n' "${#passed[@]}"
printf 'SKIPPED: %s%s\n' "${#skipped[@]}" "$([ "${#skipped[@]}" -gt 0 ] && printf '  [%s]' "${skipped[*]}")"
printf 'FAILED:  %s%s\n' "${#failed[@]}" "$([ "${#failed[@]}" -gt 0 ] && printf '  [%s]' "${failed[*]}")"
if [ "${#skipped[@]}" -gt 0 ]; then
  printf '\nTo unlock skipped scenarios:\n'
  printf '  nat/bridged networking:  scripts/dev/microagent-e2e-linux-network-setup.sh, then re-run\n'
  printf '  named-network (netpriv): microagent host setup-networking, then run as root\n'
fi
if [ "${#failed[@]}" -gt 0 ]; then
  exit 1
fi
printf 'microagent E2E suite OK\n'
