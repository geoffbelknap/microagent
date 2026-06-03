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
# This is a host probe: it requires a real Firecracker host plus a llama.cpp
# `llama-server` binary, so it must run OUTSIDE sandboxed CI (same rule as the
# other firecracker-*-host e2e lanes). It skips cleanly when prerequisites are
# absent.
#
# Required env:
#   MICROAGENT_FIRECRACKER             path to the firecracker binary
#   MICROAGENT_LLAMA_SERVER            path to llama.cpp's llama-server binary
# Optional env:
#   MICROAGENT_FIRECRACKER_SUPERVISOR  path to a prepared firecracker supervisor
#   MICROAGENT_CLI                     microagent CLI (default: .build/dev/microagent)
#   MICROAGENT_E2E_MODEL_REF           HuggingFace GGUF ref (default: Qwen 0.5B Q4)
#   LD_LIBRARY_PATH                    must include llama-server's shared libs
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLI="${MICROAGENT_CLI:-$ROOT/.build/dev/microagent}"
MODEL_REF="${MICROAGENT_E2E_MODEL_REF:-Qwen/Qwen2.5-0.5B-Instruct-GGUF/qwen2.5-0.5b-instruct-q4_k_m.gguf}"
IMAGE="docker.io/curlimages/curl:latest"

skip() { echo "SKIP microagent-e2e-model: $1" >&2; exit 0; }
fail() { echo "FAIL microagent-e2e-model: $1" >&2; exit 1; }

[ -x "$CLI" ] || skip "CLI not found at $CLI (run scripts/dev/build-local.sh)"
[ -n "${MICROAGENT_FIRECRACKER:-}" ] && [ -x "${MICROAGENT_FIRECRACKER:-/nonexistent}" ] || skip "MICROAGENT_FIRECRACKER not set/executable"
[ -n "${MICROAGENT_LLAMA_SERVER:-}" ] && [ -x "${MICROAGENT_LLAMA_SERVER:-/nonexistent}" ] || skip "MICROAGENT_LLAMA_SERVER not set/executable"
[ -e /dev/kvm ] || skip "/dev/kvm not available"

echo "microagent-e2e-model: model=$MODEL_REF image=$IMAGE"

# Ensure the model is in the store (auto-pull is part of run --model, but pull up
# front keeps the timed run fast and fails early on network problems).
if ! "$CLI" model ls 2>/dev/null | grep -q "$(printf '%s' "$MODEL_REF" | sed 's#.*/##')"; then
  echo "microagent-e2e-model: pulling $MODEL_REF ..."
  "$CLI" model pull "$MODEL_REF" >/dev/null 2>&1 || fail "model pull failed"
fi

# Guest script: retry until the model endpoint answers over the vsock bridge.
GUEST='echo "GUEST_MODEL_URL=$MICROAGENT_MODEL_URL"; for i in $(seq 1 20); do R=$(curl -s "$MICROAGENT_MODEL_URL/chat/completions" -H "Content-Type: application/json" -d "{\"messages\":[{\"role\":\"user\",\"content\":\"Reply with exactly: PONG\"}],\"max_tokens\":16,\"temperature\":0}"); case "$R" in *choices*) echo "E2E_RESPONSE: $R"; exit 0;; esac; sleep 1; done; echo "E2E_FAIL: model unreachable from guest"; exit 1'

OUT="$("$CLI" run --backend firecracker --model "$MODEL_REF" --rm "$IMAGE" -- sh -c "$GUEST" 2>&1)"
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
