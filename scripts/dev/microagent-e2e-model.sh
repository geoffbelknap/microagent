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
exit 0
