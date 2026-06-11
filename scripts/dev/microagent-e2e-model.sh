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
#   LD_LIBRARY_PATH                    must include llama-server's shared libs
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"
CLI="${MICROAGENT_CLI:-$ROOT/.build/dev/microagent}"
MODEL_REF="${MICROAGENT_E2E_MODEL_REF:-Qwen/Qwen2.5-0.5B-Instruct-GGUF/qwen2.5-0.5b-instruct-q4_k_m.gguf}"
IMAGE="docker.io/curlimages/curl:latest"

skip() { e2e_skip "microagent-e2e-model: $1"; }
fail() { echo "FAIL microagent-e2e-model: $1" >&2; exit 1; }

if [ ! -x "$CLI" ]; then
  skip "CLI not found at $CLI (run scripts/dev/build-local.sh)"
fi
if [ -z "${MICROAGENT_LLAMA_SERVER:-}" ] || [ ! -x "${MICROAGENT_LLAMA_SERVER:-/nonexistent}" ]; then
  skip "MICROAGENT_LLAMA_SERVER not set/executable"
fi

default_backend() {
  case "$(uname -s):$(uname -m)" in
    Linux:x86_64|Linux:amd64)
      printf '%s\n' firecracker
      ;;
    Darwin:arm64)
      printf '%s\n' applevf
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
  *)
    skip "unsupported host/backend for model E2E: os=$(uname -s) arch=$(uname -m) backend=$BACKEND"
    ;;
esac

echo "microagent-e2e-model: backend=$BACKEND model=$MODEL_REF image=$IMAGE"

# Ensure the model is in the store (auto-pull is part of run --model, but pull up
# front keeps the timed run fast and fails early on network problems).
if ! "$CLI" model ls 2>/dev/null | grep -q "$(printf '%s' "$MODEL_REF" | sed 's#.*/##')"; then
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
