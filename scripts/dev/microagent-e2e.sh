#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

# Each entry: name:script:platform:requirement:tier
#   platform    = all | linux | darwin (selected only on a matching host)
#   requirement = none | vm
#     none    - always runnable (no microVM boot needed)
#     vm      - needs a microVM backend (skip-with-reason when absent)
#   tier        = portable | core | broad | optional | quarantine
#     portable  - no-VM scenarios (run on every PR in the portable job)
#     core      - eleven backend-neutral VM scenarios run on every PR
#     broad     - remaining VM scenarios (nightly / release)
#     optional  - expensive or externally provisioned scenarios, run on demand
#     quarantine - temporarily disabled scenarios
SCENARIOS=(
  "coverage-matrix:scripts/dev/microagent-e2e-coverage-matrix.sh:all:none:portable"
  "contract:scripts/dev/runtime-contract-smoke.sh:all:none:portable"
  "help-usage:scripts/dev/microagent-e2e-help-usage.sh:all:none:portable"
  "mcp-stdio:scripts/dev/microagent-e2e-mcp.sh:all:none:portable"
  "registry-auth:scripts/dev/microagent-e2e-registry-auth.sh:all:none:portable"
  "text-output:scripts/dev/microagent-e2e-text-output.sh:all:none:portable"
  "init:scripts/dev/microagent-e2e-init.sh:all:none:portable"
  "mcp-lifecycle:scripts/dev/microagent-e2e-mcp-lifecycle.sh:all:vm:core"
  "survive-reboot:scripts/dev/microagent-e2e-survive-reboot.sh:all:vm:broad"
  "public-surface:scripts/dev/microagent-e2e-public-surface.sh:all:vm:broad"
  "lifecycle-deep:scripts/dev/microagent-e2e-lifecycle.sh:all:vm:core"
  "networking-deep:scripts/dev/microagent-e2e-networking-contract.sh:all:vm:core"
  "transport-deep:scripts/dev/microagent-e2e-transport.sh:all:vm:core"
  "supervision-deep:scripts/dev/microagent-e2e-supervision-contract.sh:all:vm:core"
  "dispatch:scripts/dev/microagent-e2e-dispatch.sh:linux:vm:core"
  "cred-swap:scripts/dev/microagent-e2e-cred-swap.sh:linux:vm:core"
  "cred-swap-oauth2:scripts/dev/microagent-e2e-cred-swap-oauth2.sh:linux:vm:core"
  "broker:scripts/dev/microagent-e2e-broker.sh:linux:vm:core"
  "broker-multi:scripts/dev/microagent-e2e-broker-multi.sh:linux:vm:core"
  "egress-signals:scripts/dev/microagent-e2e-egress-signals.sh:linux:vm:broad"
  "volumes:scripts/dev/microagent-e2e-volumes.sh:all:vm:broad"
  "commit-images:scripts/dev/microagent-e2e-commit.sh:all:vm:broad"
  "secrets:scripts/dev/microagent-e2e-secrets.sh:all:vm:core"
  "health:scripts/dev/microagent-e2e-health.sh:all:vm:broad"
  "exec-stream:scripts/dev/microagent-e2e-exec-stream.sh:all:vm:core"
  "model-serving:scripts/dev/microagent-e2e-model.sh:all:vm:broad"
  "model-mediation:scripts/dev/microagent-e2e-model-mediation.sh:linux:vm:optional"
  "model-mediation-runner:scripts/dev/microagent-e2e-model-mediation-runner.sh:linux:vm:optional"
  "model-mediation-runner-fake:scripts/dev/microagent-e2e-model-mediation-runner-fake.sh:linux:vm:broad"
  "model-mediation-pressure-ci:scripts/dev/microagent-e2e-model-mediation-pressure-ci.sh:linux:vm:broad"
  "model-mediation-llamacpp:scripts/dev/microagent-e2e-model-mediation-llamacpp.sh:linux:vm:optional"
  "model-mediation-vllm:scripts/dev/microagent-e2e-model-mediation-vllm.sh:linux:vm:optional"
  "firecracker-lifecycle-host:scripts/dev/microagent-e2e-lifecycle-matrix.sh:linux:vm:broad"
  "firecracker-transport-host:scripts/dev/microagent-e2e-mediation.sh:linux:vm:broad"
  "firecracker-supervision-host:scripts/dev/microagent-e2e-supervision.sh:linux:vm:broad"
  "applevf-boot:scripts/dev/applevf-live-boot-smoke.sh:darwin:vm:broad"
  "applevf-direct-console:scripts/dev/applevf-direct-console-smoke.sh:darwin:vm:broad"
  "applevf-substrate:scripts/dev/applevf-substrate-smoke.sh:darwin:vm:broad"
  "applevf-workspace-connect:scripts/dev/applevf-workspace-connect-smoke.sh:darwin:vm:broad"
  "applevf-network-mode:scripts/dev/applevf-network-mode-smoke.sh:darwin:vm:broad"
  "applevf-publish:scripts/dev/applevf-publish-smoke.sh:darwin:vm:broad"
  "applevf-vsock-diagnostic:scripts/dev/applevf-vsock-diagnostic-smoke.sh:darwin:vm:broad"
  "applevf-save-restore-config:scripts/dev/applevf-save-restore-config-check.sh:darwin:vm:broad"
  "applevf-snapshot:scripts/dev/applevf-snapshot-smoke.sh:darwin:vm:broad"
)

# Each entry: scenario|coverage|backends|feature summary.
#   coverage = portable | backend-neutral | backend-specific | host-specific
#   backends = none | host-default | linux-kvm,apple-vf | linux-kvm | apple-vf
# This is a coverage inventory, not the release support policy. See
# docs/concepts/backends.md for supported and compatibility host tiers.
SCENARIO_COVERAGE=(
  "coverage-matrix|portable|none|E2E feature inventory and scenario metadata"
  "contract|portable|none|runtime contract, synthetic state/result/artifacts"
  "help-usage|portable|none|help, usage errors, unsupported container-style flags"
  "mcp-stdio|portable|none|serve mcp, initialize, tools/list, ping, describe"
  "mcp-lifecycle|backend-neutral|linux-kvm,apple-vf|serve mcp workspace create/start/exec/halt/delete with CLI parity"
  "registry-auth|portable|none|registry credentials and private OCI pull auth"
  "text-output|portable|none|human output mode for stable public CLI surfaces"
  "init|portable|none|init scaffold, providers, --force, generated spec validation"
  "survive-reboot|host-specific|host-default|supervise --install/--uninstall boot units; no real reboot"
  "public-surface|backend-neutral|linux-kvm,apple-vf|version, contract, profiles, host, doctor, kernel, rootfs, run/result, artifact, perf, image prune"
  "lifecycle-deep|backend-neutral|linux-kvm,apple-vf|create/start/status/list/connect/logs/events/stats/halt/quarantine/clone/cp/artifact/image/delete"
  "networking-deep|backend-neutral|linux-kvm,apple-vf|network modes, publish, apply, quarantine, cached image/network paths"
  "transport-deep|backend-neutral|linux-kvm,apple-vf|mediation and vsock transport contract"
  "supervision-deep|backend-neutral|linux-kvm,apple-vf|restart supervision, signal, failure, cleanup"
  "dispatch|backend-specific|linux-kvm|one-shot delegated work in a fresh isolated workspace; returns guest result plus the mediator-written egress audit receipt, then tears down"
  "cred-swap|backend-specific|linux-kvm|--cred-swap <provider> generates a per-workspace swap config, boots under strict egress (proving the mediator loaded it), and persists the resolved entry + allowlisted provider host"
  "cred-swap-oauth2|backend-specific|linux-kvm|a Firecracker guest sends two placeholder-authenticated TLS requests through the real MITM path to hermetic token/resource services; the mediator acquires once, injects the minted bearer twice, reuses its cache, and keeps both credentials out of guest-visible state and audit"
  "broker|backend-specific|linux-kvm|--broker-upstream/--broker-secret serve the egress broker on a workspace vsock listener; an isolated-network guest reaches the upstream with the credential injected host-side and never present in the guest, trail, or manifest"
  "broker-multi|backend-specific|linux-kvm|repeatable --broker-endpoint declares multiple broker endpoints in one workspace; one isolated-network guest reaches TWO upstreams through TWO endpoints, each injecting its own credential host-side, never present in the guest, shared trail, or manifest"
  "egress-signals|backend-specific|linux-kvm|live direct-IP, HTTP/3 QUIC destination mediation, and MITM warning signals"
  "volumes|backend-neutral|linux-kvm,apple-vf|volume create/list/status/delete, attach persistence, single attach"
  "commit-images|backend-neutral|linux-kvm,apple-vf|commit stopped rootfs into local OCI image layout"
  "secrets|backend-neutral|linux-kvm,apple-vf|secret check, materialized secrets, on-demand secrets, audit records"
  "health|backend-neutral|linux-kvm,apple-vf|health.exec validation and supervise restart on unhealthy probe"
  "exec-stream|backend-neutral|linux-kvm,apple-vf|structured exec streaming, non-zero exit propagation, buffered parity"
  "model-serving|backend-neutral|linux-kvm,apple-vf|model pull/list/stop and run --model over backend vsock bridge"
  "model-mediation|host-specific|linux-kvm|Opt-in production run --model mediation matrix with a stub OpenAI-compatible runner"
  "model-mediation-runner|host-specific|linux-kvm|Opt-in runner-neutral production run --model mediation matrix for a prepared OpenAI-compatible runner"
  "model-mediation-runner-fake|host-specific|linux-kvm|Opt-in runner-neutral production run --model mediation matrix through a fake custom runner; no GPU, llama.cpp, vLLM, or HuggingFace access"
  "model-mediation-pressure-ci|host-specific|linux-kvm|CI-safe required-gate pressure target through the fake custom runner; no GPU, llama.cpp, vLLM, or HuggingFace access"
  "model-mediation-llamacpp|host-specific|linux-kvm|Opt-in production run --model mediation matrix with the llama.cpp runner"
  "model-mediation-vllm|host-specific|linux-kvm|Opt-in production run --model mediation matrix with a real vLLM GPU runner"
  "firecracker-lifecycle-host|backend-specific|linux-kvm|Firecracker lifecycle host mechanics"
  "firecracker-transport-host|backend-specific|linux-kvm|Firecracker /dev/vhost-vsock and helper mechanics"
  "firecracker-supervision-host|backend-specific|linux-kvm|Firecracker helper PID cleanup mechanics"
  "applevf-boot|backend-specific|apple-vf|Apple VF boot smoke"
  "applevf-direct-console|backend-specific|apple-vf|Apple VF direct supervisor console input"
  "applevf-substrate|backend-specific|apple-vf|Apple VF lifecycle substrate smoke"
  "applevf-workspace-connect|backend-specific|apple-vf|Apple VF connect/logs/ps smoke"
  "applevf-network-mode|backend-specific|apple-vf|Apple VF user/isolated network modes"
  "applevf-publish|backend-specific|apple-vf|Apple VF TCP publish forwarding"
  "applevf-vsock-diagnostic|backend-specific|apple-vf|Apple VF mediation and virtio-vsock diagnostics"
  "applevf-save-restore-config|backend-specific|apple-vf|Apple VF VZ save/restore configuration support probe"
  "applevf-snapshot|backend-specific|apple-vf|Apple VF snapshot create/restore/fork smoke"
)

# Each entry: feature|classification|covered backends|covering scenarios|notes.
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
  "run/create/start|backend-neutral|linux-kvm,apple-vf|public-surface,lifecycle-deep,health,volumes,model-serving|Core workspace boot paths"
  "status/list|backend-neutral|linux-kvm,apple-vf|public-surface,lifecycle-deep,applevf-workspace-connect|State/readiness/list surfaces"
  "result/logs/artifact|backend-neutral|linux-kvm,apple-vf|contract,public-surface,lifecycle-deep|Structured result, serial logs, declared artifacts"
  "events/stats|backend-neutral|linux-kvm,apple-vf|lifecycle-deep|Lifecycle event history and resource sampling"
  "connect|backend-neutral|linux-kvm,apple-vf|lifecycle-deep,applevf-workspace-connect|Interactive and send-mode console paths"
  "exec|backend-neutral|linux-kvm,apple-vf|health,exec-stream,secrets,volumes|Structured exec and streaming exec"
  "halt/quarantine/kill/delete|backend-neutral|linux-kvm,apple-vf|public-surface,lifecycle-deep,supervision-deep|Lifecycle controls and cleanup (stop is an alias of halt)"
  "clone/cp|backend-neutral|linux-kvm,apple-vf|lifecycle-deep|Stopped workspace copy and clone semantics"
  "apply|backend-neutral|linux-kvm,apple-vf|networking-deep|Supported spec changes"
  "network status/modes/publish|backend-neutral|linux-kvm,apple-vf|networking-deep,applevf-network-mode,applevf-publish|user/isolated modes plus backend publish mechanics"
  "egress signals and QUIC|backend-specific|linux-kvm|egress-signals|real guest direct-IP signal, HTTP/3 request, QUIC SNI audit, and MITM warning"
  "volume create/list/status/delete|backend-neutral|linux-kvm,apple-vf|volumes|Managed ext4 volume lifecycle and attach semantics"
  "commit/image|backend-neutral|linux-kvm,apple-vf|commit-images,lifecycle-deep,public-surface|Local OCI image records, tag/delete/prune, commit"
  "registry auth|portable|none|registry-auth|Private registry credential discovery"
  "secrets|backend-neutral|linux-kvm,apple-vf|secrets|Secret reference validation, materialized/on-demand delivery, audit"
  "health|backend-neutral|linux-kvm,apple-vf|health|Exec probes and supervise restart"
  "supervise|backend-neutral|linux-kvm,apple-vf|supervision-deep,health,survive-reboot|Restart loop plus host boot-unit generation"
  "snapshot/pause/resume|backend-neutral|linux-kvm,apple-vf|firecracker-lifecycle-host,lifecycle-deep,applevf-snapshot|Pause/resume and memory snapshot create/restore/fork on supported backends"
  "model|backend-neutral|linux-kvm,apple-vf|model-serving,model-mediation,model-mediation-runner,model-mediation-runner-fake,model-mediation-pressure-ci,model-mediation-llamacpp,model-mediation-vllm|Model store and run --model vsock pairing; mediation has stub, fake custom runner, runner-neutral, CI-safe pressure, llama.cpp, and vLLM opt-in matrices"
  "perf|backend-neutral|linux-kvm,apple-vf|public-surface|Full readiness across cold boot, snapshot fork/restore, paused resume, and guest interfaces; steady-state and footprint measurements"
  "serve mcp|portable|none|mcp-stdio|MCP stdio transport and capability manifest"
  "serve mcp lifecycle|backend-neutral|linux-kvm,apple-vf|mcp-lifecycle|Workspace lifecycle driven through MCP tools with CLI parity"
  "AX/text output|portable|none|text-output,mcp-stdio|Structured AX and human text output contracts"
)

usage() {
  cat <<'EOF'
microagent-e2e.sh

Runs the full microagent end-to-end suite for the current host backend.

Usage:
  scripts/dev/microagent-e2e.sh [--keep] [--image-cache-policy auto|refresh|require] [scenario...]
  scripts/dev/microagent-e2e.sh --list
  scripts/dev/microagent-e2e.sh --list-tier TIER
  scripts/dev/microagent-e2e.sh --list-tier-platform TIER PLATFORM
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
                     Defaults to Firecracker on Linux and Apple VF on macOS;
                     override with MICROAGENT_E2E_BACKEND=linux-kvm|apple-vf.
  networking-deep    Backend-neutral networking feature contract. Covers modes,
                     publish, cached NATS/rootfs, apply, artifacts,
                     halt/resume, quarantine, and invalid config paths where
                     each backend has matching semantics.
  transport-deep     Backend-neutral mediation/vsock transport feature contract.
  supervision-deep   Backend-neutral restart supervision, signal, failure, and
                     cleanup feature contract.
  dispatch           One-shot delegated work in a fresh, isolated workspace:
                     guest result plus the mediator-written egress audit
                     receipt, then teardown.
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
                     backend vsock bridge (Firecracker on Linux or Apple VF on
                     macOS).
  model-mediation    Opt-in Linux host backend matrix for run --model
                     mediation. Set MICROAGENT_E2E_MODEL_MEDIATION=1.
  model-mediation-runner
                    Opt-in Linux host backend matrix for run --model
                    mediation against a prepared OpenAI-compatible
                    runner. Set MICROAGENT_E2E_MODEL_MEDIATION_RUNNER=1 and
                    MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_MODEL_REF.
  model-mediation-runner-fake
                    Opt-in Linux host backend matrix for run --model
                    mediation through the custom runner contract with a fake
                    OpenAI-compatible runner. Set
                    MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE=1.
  model-mediation-pressure-ci
                    CI-safe Linux host backend pressure target for the
                    run --model mediator through the fake custom runner. Uses
                    required gates and emits pressure-decision.*.
  model-mediation-llamacpp
                    Opt-in Linux host backend matrix for run --model mediation
                    against the llama.cpp runner. Set
                    MICROAGENT_E2E_MODEL_MEDIATION_LLAMA=1.
  model-mediation-vllm
                    Opt-in Linux host backend matrix for run --model mediation
                    against a real vLLM GPU runner. Set
                    MICROAGENT_E2E_MODEL_MEDIATION_VLLM=1.
  survive-reboot     supervise --install/--uninstall boot-unit generation
                     (systemd user unit / launchd plist); no real reboot.
  firecracker-lifecycle-host
                    Firecracker/Linux host mechanics probe behind lifecycle.
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
                    Apple VF user/isolated network mode check and outbound smoke
  applevf-publish    Apple VF TCP publish forwarding smoke
  applevf-vsock-diagnostic
                    Apple VF mediation and virtio-vsock diagnostic smoke
  applevf-save-restore-config
                    Apple VF VZ save/restore configuration support probe
  applevf-snapshot  Apple VF snapshot create/restore/fork smoke

Environment:
  --keep or MICROAGENT_E2E_KEEP=1 keeps failed and successful scenario state directories.
  MICROAGENT_E2E_IMAGE=<ref> overrides the default BusyBox public-surface image.
  MICROAGENT_NATS_IMAGE=<ref> overrides the default NATS image used by scenarios.
  MICROAGENT_E2E_CACHE_DIR=<dir> overrides the shared Go build/module cache.
  MICROAGENT_E2E_IMAGE_CACHE_DIR=<dir> overrides the persistent E2E image cache.
  MICROAGENT_ROOTFS_BASE_CACHE_DIR=<dir> relocates the persistent rootfs base cache;
    set it to an empty value to disable caching for the run.
  DOCKER_CONFIG=<dir> overrides registry credential discovery for image pulls.
    When unset, the suite uses an empty test-local Docker config for public
    image scenarios so host credential-helper state cannot break E2E pulls.
    The registry-auth scenario preserves the original DOCKER_CONFIG state.
  MICROAGENT_E2E_IMAGE_CACHE_POLICY=auto|refresh|require controls persistent
    E2E image cache use for scenarios that support it.
  MICROAGENT_E2E_REFRESH_IMAGE_CACHE=1 refreshes cached E2E image rootfs files
    for compatibility with older validation commands.
  MICROAGENT_E2E_BACKEND=linux-kvm|apple-vf selects the backend
    lane for backend-agnostic feature scenarios.
  MICROAGENT_E2E_MODEL_MEDIATION=1 opts into the production run --model
    mediation matrix with a stub OpenAI-compatible runner. It does not require
    a GPU or llama.cpp.
  MICROAGENT_E2E_MODEL_MEDIATION_OUT_DIR=<dir> stores model-mediation reports.
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_POLICY_ONLY=1 runs generated policy
    validation/evaluation without KVM, Firecracker, a guest image, or a model runner.
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE=1 opts into the runner-neutral
    custom runner mediation matrix with a fake OpenAI-compatible runner.
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_PRESSURE=1 runs the fake runner
    scenario through the runner-neutral mediation pressure probe instead of the
    functional allow/deny matrix.
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_PRESSURE_PRESET=ci runs a short
    fake-runner pressure profile with required gates for CI-style validation.
    The named model-mediation-pressure-ci scenario sets these fake-runner
    pressure env vars automatically.
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_OUT_DIR=<dir> stores fake runner
    mediation reports.
  MICROAGENT_E2E_MODEL_MEDIATION_LLAMA=1 opts into the production run --model
    mediation matrix with llama.cpp. It defaults to CPU execution.
  MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_PRESSURE=1 runs the llama.cpp scenario
    through the runner-neutral mediation pressure probe.
  MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_PRESSURE_PRESET=hardware runs a bounded
    one-workspace hardware profile with warn gates and runner/GPU telemetry.
  MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_GPU=1 opts that scenario into llama.cpp
    GPU runner args.
  MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_IMAGE=<ref> overrides the guest curl image.
  MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_OUT_DIR=<dir> stores llama.cpp reports.
  MICROAGENT_E2E_MODEL_MEDIATION_VLLM=1 opts into the production run --model
    mediation matrix with a real vLLM GPU runner.
  MICROAGENT_E2E_MODEL_MEDIATION_VLLM_PRESSURE=1 runs the vLLM scenario through
    the runner-neutral mediation pressure probe.
  MICROAGENT_E2E_MODEL_MEDIATION_VLLM_PRESSURE_PRESET=hardware runs a bounded
    one-workspace hardware profile with warn gates and runner/GPU telemetry.
  MICROAGENT_E2E_MODEL_MEDIATION_VLLM_REPO=<dir> points at a vLLM checkout.
  MICROAGENT_E2E_MODEL_MEDIATION_VLLM_IMAGE=<ref> overrides the guest curl image.
  MICROAGENT_E2E_MODEL_MEDIATION_VLLM_OUT_DIR=<dir> stores vLLM reports.
  MICROAGENT_E2E_HEARTBEAT=<seconds> sets the "still running" heartbeat interval
    for long scenarios (default 20; scenarios faster than this stay quiet).

Scenarios that need a microVM backend skip with a reason when the host lacks the
prerequisite; a preflight line and a final PASSED/SKIPPED/FAILED summary report
what was validated.
  MICROAGENT_FIRECRACKER_SUPERVISOR=<path> uses a prepared supervisor binary.
  MICROAGENT_APPLEVF_SUPERVISOR=<path> uses a prepared Apple VF supervisor binary
    and skips the source-checkout supervisor refresh.
  MICROAGENT_APPLEVF_KERNEL=<path> uses a prepared Apple VF Linux ARM64 kernel.

Apple VF VM scenarios refresh the repository-owned supervisor from current
source before preflight. Set MICROAGENT_APPLEVF_SUPERVISOR to validate a
specific prepared binary instead.
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

# scenario_field <name> <index>: print field 2=script, 3=platform, 4=requirement, 5=tier.
scenario_field() {
  wanted="$(canonical_scenario "$1")"
  for entry in "${SCENARIOS[@]}"; do
    f_name="${entry%%:*}"
    if [ "$f_name" = "$wanted" ]; then
      rest="${entry#*:}"
      f_script="${rest%%:*}"
      rest="${rest#*:}"
      f_platform="${rest%%:*}"
      rest="${rest#*:}"
      f_req="${rest%%:*}"
      f_tier="${rest#*:}"
      case "$2" in
        2) printf '%s\n' "$f_script" ;;
        3) printf '%s\n' "$f_platform" ;;
        4) printf '%s\n' "${f_req:-vm}" ;;
        5) printf '%s\n' "${f_tier:-broad}" ;;
      esac
      return 0
    fi
  done
  return 1
}

scenario_script() { scenario_field "$1" 2; }
scenario_platform() { scenario_field "$1" 3; }
scenario_requirement() { scenario_field "$1" 4; }
scenario_tier() { scenario_field "$1" 5; }

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
      rest="${rest#*:}"
      req="${rest%%:*}"
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
    rest="${rest#*:}"
    req="${rest%%:*}"
    printf '%-28s %-8s %-8s %-16s %-20s %s\n' \
      "$name" "$platform" "${req:-vm}" \
      "$(scenario_meta "$name" coverage)" \
      "$(scenario_meta "$name" backends)" \
      "$(scenario_meta "$name" features)"
  done
}

list_matrix() {
  if [ "${MICROAGENT_E2E_MATRIX_TSV:-0}" = "1" ]; then
    printf 'FEATURE\tCLASS\tBACKENDS\tSCENARIOS\tNOTES\n'
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
  printf '%-30s %-18s %-22s %-44s %s\n' "FEATURE" "CLASS" "BACKENDS" "SCENARIOS" "NOTES"
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
    --list-tier)
      if [ "$#" -lt 2 ]; then
        echo "--list-tier requires a tier name (portable, core, broad, optional, quarantine)" >&2
        exit 2
      fi
      _list_tier="$2"
      shift 2
      for _entry in "${SCENARIOS[@]}"; do
        _lname="${_entry%%:*}"
        if [ "$(scenario_tier "$_lname")" = "$_list_tier" ]; then
          printf '%s\n' "$_lname"
        fi
      done
      exit 0
      ;;
    --list-tier-platform)
      if [ "$#" -lt 3 ]; then
        echo "--list-tier-platform requires a tier and platform (all, linux, darwin)" >&2
        exit 2
      fi
      _list_tier="$2"
      _list_platform="$3"
      case "$_list_platform" in
        all|linux|darwin) ;;
        *)
          echo "unknown platform: $_list_platform (expected all, linux, or darwin)" >&2
          exit 2
          ;;
      esac
      shift 3
      for _entry in "${SCENARIOS[@]}"; do
        _lname="${_entry%%:*}"
        _platform="$(scenario_platform "$_lname")"
        if [ "$(scenario_tier "$_lname")" = "$_list_tier" ] &&
          { [ "$_platform" = "all" ] || [ "$_platform" = "$_list_platform" ]; }; then
          printf '%s\n' "$_lname"
        fi
      done
      exit 0
      ;;
    --list-tier=*)
      _list_tier="${1#*=}"
      shift
      for _entry in "${SCENARIOS[@]}"; do
        _lname="${_entry%%:*}"
        if [ "$(scenario_tier "$_lname")" = "$_list_tier" ]; then
          printf '%s\n' "$_lname"
        fi
      done
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
  export MICROAGENT_KEEP_MICROAGENT_E2E_MODEL_MEDIATION=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_MODEL_MEDIATION_LLAMA=1
  export MICROAGENT_KEEP_MICROAGENT_E2E_MODEL_MEDIATION_VLLM=1
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

# A source checkout's Go scenarios build a fresh CLI, so letting them silently
# reuse an older Swift supervisor produces a mixed-revision Apple VF test. That
# can turn an already-fixed supervisor defect into an apparent regression on a
# clean Go commit. Refresh the repository-owned default once before any
# supported VM scenario. An explicit override is a caller-owned prepared
# artifact and remains untouched.
if [ "$(uname -s)" = "Darwin" ] && [ "$(uname -m)" = "arm64" ] &&
  [ -z "${MICROAGENT_APPLEVF_SUPERVISOR:-}" ]; then
  refresh_applevf_supervisor=0
  for name in "${selected[@]}"; do
    if scenario_supported "$name" && [ "$(scenario_requirement "$name")" = "vm" ]; then
      refresh_applevf_supervisor=1
      break
    fi
  done
  if [ "$refresh_applevf_supervisor" = "1" ]; then
    printf 'microagent E2E preflight: refreshing Apple VF supervisor from current source\n'
    "$ROOT/scripts/dev/applevf-supervisor-build.sh" >/dev/null
    export MICROAGENT_APPLEVF_SUPERVISOR="$ROOT/supervisors/applevf/.build/release/microagent-applevf-supervisor"
  fi
fi

# Preflight: probe host capabilities once so the summary explains skips.
have_vm=no; e2e_have_vm && have_vm=yes
is_wsl=no; e2e_is_wsl && is_wsl=yes
printf 'microagent E2E preflight: os=%s arch=%s wsl=%s vm=%s\n' \
  "$(e2e_friendly_os)" "$(uname -m)" "$is_wsl" "$have_vm"
if [ "$have_vm" = "no" ]; then
  printf '  (no microVM backend: vm scenarios will SKIP. Run microagent doctor for details.)\n'
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
if [ "${#failed[@]}" -gt 0 ]; then
  exit 1
fi
printf 'microagent E2E suite OK\n'
