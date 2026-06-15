#!/usr/bin/env bash
#
# microagent-e2e-model.sh — live end-to-end check for local model serving.
#
# Boots a real microVM with `microagent run --model <ref>` and asserts that an
# in-guest OpenAI client reaches the host-served model purely over the vsock
# bridge (no in-VM network is used for the model path). Exercises the whole
# Phase 1-3 stack: the model store, the host llama.cpp runner, the workspace
# vsock transport, the guest forwarder, and the `run --model` orchestration.
#
# Then exercises persistent workspace pairing: `create --model` persists the
# canonical ref in the manifest, every `start` re-pairs (runner up, holder
# added, env + bridge live in the guest), and halt/delete release the holder.
#
# This is a host probe: it requires a real microVM backend plus a llama.cpp
# `llama-server` binary, so it must run OUTSIDE sandboxed CI. It skips cleanly
# when prerequisites are absent.
#
# Required env:
#   MICROAGENT_LLAMA_SERVER            path to llama.cpp's llama-server binary
# Optional env:
#   MICROAGENT_E2E_BACKEND             firecracker|applevf
#   MICROAGENT_FIRECRACKER             path to the firecracker binary
#   MICROAGENT_FIRECRACKER_SUPERVISOR  path to a prepared firecracker supervisor
#   MICROAGENT_APPLEVF_SUPERVISOR      path to a prepared Apple VF supervisor
#   MICROAGENT_APPLEVF_KERNEL          path to an Apple VF Linux ARM64 kernel
#   MICROAGENT_CLI                     microagent CLI (default: .build/dev/microagent)
#   MICROAGENT_E2E_MODEL_REF           HuggingFace GGUF ref (default: Qwen 0.5B Q4)
#   MICROAGENT_E2E_MODEL_GPU           off|auto|required (default: auto)
#   MICROAGENT_E2E_MODEL_GPU_PATTERN   grep -E pattern for runner log GPU evidence
#   LD_LIBRARY_PATH                    must include llama-server's shared libs
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh
. "$ROOT/scripts/dev/e2e-lib.sh"
CLI="${MICROAGENT_CLI:-$(e2e_exe "$ROOT/.build/dev/microagent")}"
MODEL_REF="${MICROAGENT_E2E_MODEL_REF:-Qwen/Qwen2.5-0.5B-Instruct-GGUF/qwen2.5-0.5b-instruct-q4_k_m.gguf}"
IMAGE="docker.io/curlimages/curl:latest"
GPU_MODE="${MICROAGENT_E2E_MODEL_GPU:-auto}"
GPU_PATTERN="${MICROAGENT_E2E_MODEL_GPU_PATTERN:-CUDA[0-9]*[[:space:]]*:|ggml_cuda|CUDA[[:space:]]*:|Metal[[:space:]]*:|Vulkan[[:space:]]*:|SYCL}"
GPU_CHECK=0

skip() { e2e_skip "microagent-e2e-model: $1"; }
fail() { echo "FAIL microagent-e2e-model: $1" >&2; exit 1; }

if [ ! -x "$CLI" ]; then
  skip "CLI not found at $CLI (run scripts/dev/build-local.sh)"
fi
if [ -z "${MICROAGENT_LLAMA_SERVER:-}" ] || [ ! -x "${MICROAGENT_LLAMA_SERVER:-/nonexistent}" ]; then
  skip "MICROAGENT_LLAMA_SERVER not set/executable"
fi

runner_args_request_gpu() {
  printf '%s' "${MICROAGENT_MODEL_RUNNER_ARGS:-}" | grep -Eq -- '(^|[^[:alnum:]_])(-ngl|--n-gpu-layers|--gpu-layers|--main-gpu|--tensor-split|--device|--gpu)([^[:alnum:]_]|$)'
}

host_runner_reports_gpu() {
  "${MICROAGENT_LLAMA_SERVER}" --list-devices 2>/dev/null | grep -Eiq "$GPU_PATTERN"
}

configure_gpu_check() {
  case "$GPU_MODE" in
    off|false|no|0)
      GPU_CHECK=0
      echo "microagent-e2e-model: GPU assertion disabled"
      ;;
    auto|"")
      if host_runner_reports_gpu && runner_args_request_gpu; then
        GPU_CHECK=1
        echo "microagent-e2e-model: GPU assertion enabled (runner reports GPU and runner args request GPU offload)"
      else
        GPU_CHECK=0
        echo "microagent-e2e-model: GPU assertion skipped (set MICROAGENT_E2E_MODEL_GPU=required to require it)"
      fi
      ;;
    required|require|true|yes|1)
      if ! host_runner_reports_gpu; then
        fail "GPU assertion required but $MICROAGENT_LLAMA_SERVER did not report a GPU matching: $GPU_PATTERN"
      fi
      GPU_CHECK=1
      echo "microagent-e2e-model: GPU assertion required"
      ;;
    *)
      fail "invalid MICROAGENT_E2E_MODEL_GPU=$GPU_MODE (expected off, auto, or required)"
      ;;
  esac
}

runner_log_for_holder() {
  "$CLI" --json model runners 2>/dev/null | python3 -c '
import json
import sys

holder = sys.argv[1]
try:
    idx = json.load(sys.stdin) or {}
except Exception:
    sys.exit(1)
for runner in idx.get("runners") or []:
    if holder in (runner.get("holders") or []):
        log_path = runner.get("log_path") or ""
        if log_path:
            print(log_path)
            sys.exit(0)
sys.exit(1)
' "$1"
}

assert_runner_gpu_log() {
  local holder="$1"
  local log_path

  if ! log_path="$(runner_log_for_holder "$holder")"; then
    "$CLI" --json model runners >&2 || true
    fail "GPU assertion enabled but no runner log path was found for holder $holder"
  fi
  if [ ! -r "$log_path" ]; then
    fail "GPU assertion enabled but runner log is not readable: $log_path"
  fi
  if grep -Eiq "$GPU_PATTERN" "$log_path"; then
    echo "PASS microagent-e2e-model: runner log contains GPU backend evidence"
    return
  fi
  echo "microagent-e2e-model: runner log missing GPU evidence matching: $GPU_PATTERN" >&2
  tail -n 80 "$log_path" >&2 || true
  fail "GPU assertion enabled but host runner did not report GPU backend evidence"
}

configure_gpu_check

default_backend() {
  case "$(uname -s):$(uname -m)" in
    Linux:x86_64|Linux:amd64)
      printf '%s\n' firecracker
      ;;
    Darwin:arm64)
      printf '%s\n' applevf
      ;;
    MINGW*:x86_64|MSYS*:x86_64|CYGWIN*:x86_64)
      printf '%s\n' windows-hyperv
      ;;
    *)
      printf '%s\n' unsupported
      ;;
  esac
}

BACKEND="${MICROAGENT_E2E_BACKEND:-$(default_backend)}"
RUN_FLAGS=(--model "$MODEL_REF" --rm "$IMAGE")

case "$BACKEND" in
  firecracker)
    if [ -z "${MICROAGENT_FIRECRACKER:-}" ] || [ ! -x "${MICROAGENT_FIRECRACKER:-/nonexistent}" ]; then
      skip "MICROAGENT_FIRECRACKER not set/executable"
    fi
    if [ ! -e /dev/kvm ]; then
      skip "/dev/kvm not available"
    fi
    RUN_FLAGS=(--backend firecracker "${RUN_FLAGS[@]}")
    CREATE_FLAGS=(--backend firecracker)
    START_FLAGS=(--backend firecracker)
    CTRL_FLAGS=(--backend firecracker)
    ;;
  applevf)
    case "$(uname -s):$(uname -m)" in
      Darwin:arm64)
        ;;
      *)
        skip "Apple VF model E2E requires macOS on Apple silicon"
        ;;
    esac
    SUPERVISOR="${MICROAGENT_APPLEVF_SUPERVISOR:-$ROOT/.build/dev/microagent-applevf-supervisor}"
    KERNEL="${MICROAGENT_APPLEVF_KERNEL:-$HOME/.microagent/kernels/apple-vf/arm64/Image}"
    if [ ! -r "$KERNEL" ] && [ -r "$HOME/.microagent/kernels/apple-vf/Image" ]; then
      KERNEL="$HOME/.microagent/kernels/apple-vf/Image"
    fi
    GUEST_INIT="${MICROAGENT_GUEST_INIT:-$ROOT/.build/dev/microagent-guestinit-arm64}"
    [ -x "$SUPERVISOR" ] || skip "Apple VF supervisor not executable at $SUPERVISOR (run scripts/dev/build-local.sh)"
    [ -r "$KERNEL" ] || skip "Apple VF kernel not readable at $KERNEL"
    [ -x "$GUEST_INIT" ] || skip "guest init not executable at $GUEST_INIT (run scripts/dev/build-local.sh)"
    RUN_FLAGS=(--backend apple-vf --supervisor "$SUPERVISOR" --kernel "$KERNEL" --guest-init "$GUEST_INIT" "${RUN_FLAGS[@]}")
    CREATE_FLAGS=(--backend apple-vf --supervisor "$SUPERVISOR" --kernel "$KERNEL" --guest-init "$GUEST_INIT")
    START_FLAGS=(--backend apple-vf --supervisor "$SUPERVISOR" --kernel "$KERNEL")
    CTRL_FLAGS=(--backend apple-vf --supervisor "$SUPERVISOR")
    ;;
  windows-hyperv)
    e2e_is_windows || skip "windows-hyperv model E2E requires a Windows host"
    e2e_have_hcs || skip "Hyper-V HCS services (vmms/vmcompute) are not running"
    KERNEL="$HOME/.microagent/kernels/windows-hyperv/amd64/Image"
    [ -r "$KERNEL" ] || skip "windows-hyperv kernel not installed at $KERNEL (run: microagent kernel install)"
    GUEST_INIT="${MICROAGENT_GUEST_INIT:-$ROOT/.build/dev/microagent-guestinit-amd64}"
    [ -e "$GUEST_INIT" ] || skip "guest init not found at $GUEST_INIT (build with GOOS=linux GOARCH=amd64)"
    # The model path rides hv_sock, not the guest NIC; isolated networking
    # keeps the scenario runnable on non-elevated hosts (no HNS NAT).
    RUN_FLAGS=(--backend windows-hyperv --guest-init "$GUEST_INIT" --network isolated --size-mib 512 "${RUN_FLAGS[@]}")
    CREATE_FLAGS=(--backend windows-hyperv --guest-init "$GUEST_INIT" --network isolated --size-mib 512)
    START_FLAGS=(--backend windows-hyperv)
    CTRL_FLAGS=(--backend windows-hyperv)
    ;;
  *)
    skip "unsupported host/backend for model E2E: os=$(uname -s) arch=$(uname -m) backend=$BACKEND"
    ;;
esac

echo "microagent-e2e-model: backend=$BACKEND model=$MODEL_REF image=$IMAGE"

# Ensure the model is in the store (auto-pull is part of run --model, but pull up
# front keeps the timed run fast and fails early on network problems).
if ! "$CLI" model list 2>/dev/null | grep -q "$(printf '%s' "$MODEL_REF" | sed 's#.*/##')"; then
  echo "microagent-e2e-model: pulling $MODEL_REF ..."
  "$CLI" model pull "$MODEL_REF" >/dev/null 2>&1 || fail "model pull failed"
fi

# Guest script: retry until the model endpoint answers over the vsock bridge.
# shellcheck disable=SC2016
GUEST='echo "GUEST_MODEL_URL=$MICROAGENT_MODEL_URL"; for i in $(seq 1 20); do R=$(curl -s "$MICROAGENT_MODEL_URL/chat/completions" -H "Content-Type: application/json" -d "{\"messages\":[{\"role\":\"user\",\"content\":\"Reply with exactly: PONG\"}],\"max_tokens\":16,\"temperature\":0}"); case "$R" in *choices*) echo "E2E_RESPONSE: $R"; exit 0;; esac; sleep 1; done; echo "E2E_FAIL: model unreachable from guest"; exit 1'

OUT="$("$CLI" run "${RUN_FLAGS[@]}" sh -c "$GUEST" 2>&1)"
RUN_RC=$?

# Best-effort cleanup of the runner the run started (a separately pinned runner,
# if any, is left alone by design).
"$CLI" model stop "$MODEL_REF" >/dev/null 2>&1 || true

if [ "$RUN_RC" -ne 0 ]; then
  echo "$OUT" >&2
  fail "run --model exited $RUN_RC"
fi
# The guest prints "E2E_RESPONSE: ..." only when the model returned a body
# containing "choices" (see the case match in GUEST). The guest's stdout is
# nested inside the run's JSON result envelope, so its quotes are escaped there;
# match the guest marker rather than the raw JSON.
if ! printf '%s' "$OUT" | grep -q 'E2E_RESPONSE'; then
  echo "$OUT" >&2
  fail "guest did not receive a valid OpenAI chat completion"
fi

echo "PASS microagent-e2e-model: guest reached the locally-served model over vsock"

# --- Workspace pairing scenario ----------------------------------------------
# create --model persists the canonical ref; every start re-pairs (runner up,
# workspace registered as holder, env + vsock bridge live in the guest);
# halt releases the holder; a second start re-pairs; delete releases for good.

WS="model-pair-e2e"

# shellcheck disable=SC2317,SC2329
ws_cleanup() {
  "$CLI" kill "$WS" "${CTRL_FLAGS[@]}" >/dev/null 2>&1 || true
  "$CLI" delete "$WS" --force --yes "${CTRL_FLAGS[@]}" >/dev/null 2>&1 || true
  "$CLI" model stop "$MODEL_REF" >/dev/null 2>&1 || true
}
trap ws_cleanup EXIT

# holders_of <workspace>: print the model_ref of every runner holding <workspace>.
holders_of() {
  "$CLI" --json model runners 2>/dev/null | python3 -c '
import json, sys
ws = sys.argv[1]
idx = json.load(sys.stdin) or {}
for r in idx.get("runners") or []:
    if ws in (r.get("holders") or []):
        print(r.get("model_ref", ""))
' "$1"
}

# Stale state from an earlier aborted run must not fail create.
"$CLI" delete "$WS" --force --yes "${CTRL_FLAGS[@]}" >/dev/null 2>&1 || true

echo "microagent-e2e-model: workspace pairing scenario (workspace=$WS)"

if ! "$CLI" create "$WS" --image "$IMAGE" --model "$MODEL_REF" "${CREATE_FLAGS[@]}" >/dev/null 2>&1; then
  fail "create --model failed"
fi
# create leaves the workspace halted: its setup-boot holder must be gone.
if [ -n "$(holders_of "$WS")" ]; then
  fail "create left a runner holder for $WS"
fi

if ! "$CLI" start "$WS" "${START_FLAGS[@]}" >/dev/null 2>&1; then
  fail "start of paired workspace failed"
fi
CANONICAL="$(holders_of "$WS")"
if [ -z "$CANONICAL" ]; then
  "$CLI" --json model runners >&2 || true
  fail "start did not register $WS as a model runner holder"
fi
echo "microagent-e2e-model: start re-paired $WS with $CANONICAL"

# exec_is_ready: read status JSON on stdin; exit 0 iff readiness.execReady.ready.
exec_is_ready() {
  python3 -c '
import json, sys
try:
    doc = json.load(sys.stdin) or {}
except ValueError:
    sys.exit(1)
ready = ((doc.get("readiness") or {}).get("execReady") or {}).get("ready")
sys.exit(0 if ready is True else 1)
'
}

# Wait for the guest exec service, then prove env + bridge from inside the guest.
exec_ready=0
for _ in $(seq 1 60); do
  if "$CLI" --json status "$WS" "${CTRL_FLAGS[@]}" 2>/dev/null | exec_is_ready; then
    exec_ready=1
    break
  fi
  sleep 1
done
if [ "$exec_ready" -ne 1 ]; then
  "$CLI" --json status "$WS" "${CTRL_FLAGS[@]}" >&2 || true
  fail "workspace exec service did not become ready"
fi

# shellcheck disable=SC2016
WS_GUEST='echo "WS_MODEL_URL=$MICROAGENT_MODEL_URL"; for i in $(seq 1 20); do R=$(curl -s "$MICROAGENT_MODEL_URL/models"); case "$R" in *object*|*data*) echo "WS_MODELS: $R"; exit 0;; esac; sleep 1; done; echo "WS_FAIL: model unreachable from guest"; exit 1'
WS_OUT="$("$CLI" exec "$WS" -- sh -c "$WS_GUEST" 2>&1)"
if ! printf '%s' "$WS_OUT" | grep -q 'WS_MODELS'; then
  echo "$WS_OUT" >&2
  fail "paired workspace could not reach the model over the vsock bridge"
fi
if [ "$GPU_CHECK" -eq 1 ]; then
  assert_runner_gpu_log "$WS"
fi

if ! "$CLI" halt "$WS" "${CTRL_FLAGS[@]}" >/dev/null 2>&1; then
  fail "halt of paired workspace failed"
fi
if [ -n "$(holders_of "$WS")" ]; then
  fail "halt did not release the model runner holder for $WS"
fi
echo "microagent-e2e-model: halt released the holder for $WS"

if ! "$CLI" start "$WS" "${START_FLAGS[@]}" >/dev/null 2>&1; then
  fail "second start of paired workspace failed"
fi
if [ -z "$(holders_of "$WS")" ]; then
  "$CLI" --json model runners >&2 || true
  fail "second start did not re-pair $WS"
fi
echo "microagent-e2e-model: second start re-paired $WS"

if ! "$CLI" delete "$WS" --force --yes "${CTRL_FLAGS[@]}" >/dev/null 2>&1; then
  fail "delete of paired workspace failed"
fi
if [ -n "$(holders_of "$WS")" ]; then
  fail "delete did not release the model runner holder for $WS"
fi
if "$CLI" --json model runners 2>/dev/null | grep -q "$CANONICAL"; then
  fail "runner for $CANONICAL still alive after delete released the last holder"
fi

echo "PASS microagent-e2e-model: workspace pairing (create/start/halt/restart/delete) verified"
exit 0
