#!/usr/bin/env bash
#
# microagent-e2e-model-mediation-llamacpp.sh - opt-in production model mediation
# check against the default llama.cpp OpenAI-compatible runner.
#
# This validates the real `run --model` path with the experimental host-worker
# mediator enabled and the default llama.cpp runner. By default it exercises the
# CPU runner behavior; set MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_GPU=1 or provide
# explicit runner args to opt into GPU offload.
#
# Required:
#   MICROAGENT_E2E_MODEL_MEDIATION_LLAMA=1
#   MICROAGENT_LLAMA_SERVER executable, unless llama-server is on PATH
#
# Optional:
#   MICROAGENT_CLI
#   MICROAGENT_FIRECRACKER
#   MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_IMAGE
#   MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_OUT_DIR
#   MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_STATE_DIR default: ~/.microagent
#   MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_KEEP
#   MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_MODEL_REF
#   MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_GPU       0/1 (default: 0)
#   MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_RUNNER_ARGS
#   MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_CHAT_TOKENS   default: 64
#   MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_STREAM_TOKENS default: 96
#   MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_SAMPLES       default: 3
#   MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_TELEMETRY     off, auto, or required (default: auto)
#   MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_GATE_MODE     off, warn, or required (default: required)
#   MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_PRESSURE      0/1 (default: 0)
#   MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_PRESSURE_PRESET
#                                                          default, baseline, ci, or hardware
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"
# shellcheck source=scripts/dev/microagent-model-mediation-pressure-presets.sh disable=SC1091
. "$ROOT/scripts/dev/microagent-model-mediation-pressure-presets.sh"

CLI="${MICROAGENT_CLI:-$(e2e_exe "$ROOT/.build/dev/microagent")}"
OUT_DIR="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_OUT_DIR:-/tmp/ma-e2e-mm-llama-$(date +%Y%m%d%H%M%S)}"
STATE_DIR="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_STATE_DIR:-$HOME/.microagent}"
KEEP_FAILED="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_KEEP:-${MICROAGENT_KEEP_MICROAGENT_E2E_MODEL_MEDIATION_LLAMA:-0}}"
IMAGE="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_IMAGE:-quay.io/curl/curl:latest}"
MODEL_REF="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_MODEL_REF:-Qwen/Qwen2.5-0.5B-Instruct-GGUF/qwen2.5-0.5b-instruct-q4_k_m.gguf}"
GPU_MODE="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_GPU:-0}"
RUNNER_ARGS_RAW="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_RUNNER_ARGS:-}"
CHAT_TOKENS="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_CHAT_TOKENS:-64}"
STREAM_TOKENS="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_STREAM_TOKENS:-96}"
SAMPLES="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_SAMPLES:-3}"
TELEMETRY="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_TELEMETRY:-auto}"
TELEMETRY_INTERVAL="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_TELEMETRY_INTERVAL:-0.5}"
TELEMETRY_ENDPOINTS="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_TELEMETRY_ENDPOINTS:-/metrics,/slots}"
GATE_MODE="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_GATE_MODE:-required}"
PRESSURE="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_PRESSURE:-0}"
PRESSURE_PRESET="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_PRESSURE_PRESET:-default}"
case "$PRESSURE" in
  1|true|TRUE|yes|YES|required) ;;
  *) PRESSURE_PRESET=default ;;
esac
PRESSURE_PREFIX="MICROAGENT_E2E_MODEL_MEDIATION_LLAMA"
PRESSURE_WORKSPACES="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_WORKSPACES 2)"
PRESSURE_CONCURRENCY="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_CONCURRENCY 1,2)"
PRESSURE_CASES="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_CASES direct,local,pf,pa)"
PRESSURE_SAMPLES="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_SAMPLES "$SAMPLES")"
PRESSURE_WARMUPS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_WARMUPS 1)"
PRESSURE_CHAT_TOKENS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_CHAT_TOKENS "$CHAT_TOKENS")"
PRESSURE_STREAM_TOKENS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_STREAM_TOKENS "$STREAM_TOKENS")"
PRESSURE_GATE_MODE="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_GATE_MODE warn)"
PRESSURE_TELEMETRY="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_TELEMETRY "$TELEMETRY")"
MAX_MODELS_TOTAL_P95_DELTA_MS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_MAX_MODELS_TOTAL_P95_DELTA_MS "${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_MAX_MODELS_TOTAL_P95_DELTA_MS:-100}")"
MAX_CHAT_TOTAL_P95_DELTA_MS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_MAX_CHAT_TOTAL_P95_DELTA_MS "${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_MAX_CHAT_TOTAL_P95_DELTA_MS:-500}")"
MAX_STREAM_TTFB_P95_DELTA_MS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_MAX_STREAM_TTFB_P95_DELTA_MS "${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_MAX_STREAM_TTFB_P95_DELTA_MS:-250}")"
MAX_DECISION_P95_MS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_MAX_DECISION_P95_MS "${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_MAX_DECISION_P95_MS:-100}")"
RUNNER_PID=""
RUNNER_PORT=""
REQUEST_MODEL=""
CANONICAL_REF=""
CTRL_FLAGS=(--backend linux-kvm --state-dir "$STATE_DIR")

skip() { e2e_skip "microagent-e2e-model-mediation-llamacpp: $1"; }
fail() { echo "FAIL microagent-e2e-model-mediation-llamacpp: $1" >&2; exit 1; }

cleanup() {
  local status=$?
  set +e
  for workspace in mml-direct mml-local mml-pa mml-pd mml-pf mml-pfd mml-pu; do
    "$CLI" kill "$workspace" "${CTRL_FLAGS[@]}" >/dev/null 2>&1 || true
    "$CLI" delete "$workspace" --force --yes "${CTRL_FLAGS[@]}" >/dev/null 2>&1 || true
  done
  if [ -n "$CANONICAL_REF" ]; then
    "$CLI" model stop "$CANONICAL_REF" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  fi
  if [ "$KEEP_FAILED" = "1" ]; then
    if [ "$status" -ne 0 ]; then
      echo "microagent-e2e-model-mediation-llamacpp: preserved failed state under $OUT_DIR" >&2
    else
      echo "microagent-e2e-model-mediation-llamacpp: preserved reports under $OUT_DIR" >&2
    fi
  else
    rm -rf "$OUT_DIR"
  fi
  exit "$status"
}
trap cleanup EXIT

runner_env_args() {
  if [ -n "${MICROAGENT_LLAMA_SERVER:-}" ]; then
    printf '%s\n' "MICROAGENT_LLAMA_SERVER=$MICROAGENT_LLAMA_SERVER"
  fi
  if [ -n "$RUNNER_ARGS_RAW" ]; then
    printf '%s\n' "MICROAGENT_MODEL_RUNNER_ARGS=$RUNNER_ARGS_RAW"
  else
    printf '%s\n' "MICROAGENT_MODEL_RUNNER_ARGS="
  fi
}

canonical_model_ref() {
  "$CLI" --json model pull "$MODEL_REF" --state-dir "$STATE_DIR" | python3 -c '
import json
import sys

doc = json.load(sys.stdin)
print(doc.get("model_ref") or sys.argv[1])
' "$MODEL_REF"
}

runner_count_for_model() {
  local model_ref="$1"
  local runners_json
  runners_json="$("$CLI" --json model runners --state-dir "$STATE_DIR" 2>/dev/null || printf '{}')"
  python3 - "$model_ref" "$runners_json" <<'PY'
import json
import sys

model_ref = sys.argv[1]
try:
    doc = json.loads(sys.argv[2]) or {}
except Exception:
    print(0)
    raise SystemExit(0)
runners = doc.get("runners") or []
print(sum(1 for runner in runners if runner.get("model_ref") == model_ref))
PY
}

discover_request_model() {
  python3 - "$RUNNER_PORT" <<'PY'
import json
import sys
import urllib.request

port = sys.argv[1]
with urllib.request.urlopen(f"http://127.0.0.1:{port}/v1/models", timeout=10) as response:
    doc = json.loads(response.read(1024 * 1024))
models = doc.get("data") if isinstance(doc, dict) else None
if not isinstance(models, list):
    raise SystemExit("model list was not OpenAI-compatible")
for model in models:
    if isinstance(model, dict) and model.get("id"):
        print(model["id"])
        raise SystemExit(0)
raise SystemExit("no model id found")
PY
}

start_pinned_runner() {
  local log="$OUT_DIR/model-serve.log"
  local runner_json="$OUT_DIR/model-serve.json"
  mapfile -t env_args < <(runner_env_args)
  if ! env "${env_args[@]}" "$CLI" --json model serve "$MODEL_REF" --state-dir "$STATE_DIR" >"$runner_json" 2>"$log"; then
    cat "$log" >&2 || true
    fail "model serve failed"
  fi
  RUNNER_PID="$(python3 - "$runner_json" <<'PY'
import json
import sys
from pathlib import Path

doc = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
print(doc["pid"])
PY
)"
  RUNNER_PORT="$(python3 - "$runner_json" <<'PY'
import json
import sys
from pathlib import Path

doc = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
print(doc["port"])
PY
)"
  REQUEST_MODEL="$(discover_request_model)"
  echo "microagent-e2e-model-mediation-llamacpp: pinned llama.cpp runner pid=$RUNNER_PID model=$REQUEST_MODEL"
}

assert_single_runner_reused() {
  local runners_json="$OUT_DIR/runners.json"
  "$CLI" --json model runners --state-dir "$STATE_DIR" >"$runners_json"
  python3 - "$runners_json" "$RUNNER_PID" <<'PY'
import json
import sys
from pathlib import Path

doc = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
want_pid = int(sys.argv[2])
runners = doc.get("runners") or []
if len(runners) != 1:
    raise SystemExit(f"runner count = {len(runners)}, want 1")
runner = runners[0]
if runner.get("pid") != want_pid:
    raise SystemExit(f"runner pid = {runner.get('pid')}, want {want_pid}")
if runner.get("engine") != "llama.cpp":
    raise SystemExit(f"runner engine = {runner.get('engine')!r}, want 'llama.cpp'")
PY
}

case "${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA:-0}" in
  1|true|TRUE|yes|YES|required)
    ;;
  *)
    skip "set MICROAGENT_E2E_MODEL_MEDIATION_LLAMA=1 to run the opt-in llama.cpp model mediation scenario"
    ;;
esac
case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    skip "llama.cpp model mediation E2E currently targets the Linux host backend"
    ;;
esac
if [ ! -x "$CLI" ]; then
  skip "CLI not found at $CLI (run scripts/dev/build-local.sh)"
fi
if [ ! -e /dev/kvm ]; then
  skip "/dev/kvm not available"
fi
if ! pressure_preset_validate "$PRESSURE_PRESET"; then
  fail "MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_PRESSURE_PRESET must be default, baseline, ci, or hardware"
fi
if [ -z "${MICROAGENT_FIRECRACKER:-}" ]; then
  MICROAGENT_FIRECRACKER="$(e2e_resolve_firecracker)" || skip "Firecracker binary not resolved"
  export MICROAGENT_FIRECRACKER
fi
if [ -z "${MICROAGENT_LLAMA_SERVER:-}" ] && ! command -v llama-server >/dev/null 2>&1; then
  skip "llama-server not found; set MICROAGENT_LLAMA_SERVER"
fi
for numeric in CHAT_TOKENS STREAM_TOKENS SAMPLES; do
  value="${!numeric}"
  case "$value" in
    ''|*[!0-9]*) fail "$numeric must be a positive integer" ;;
  esac
  if [ "$value" -le 0 ]; then
    fail "$numeric must be a positive integer"
  fi
done
case "$TELEMETRY" in
  off|auto|required) ;;
  *) fail "MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_TELEMETRY must be off, auto, or required" ;;
esac
case "$GATE_MODE" in
  off|warn|required) ;;
  *) fail "MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_GATE_MODE must be off, warn, or required" ;;
esac
case "$GPU_MODE" in
  1|true|TRUE|yes|YES)
    if [ -z "$RUNNER_ARGS_RAW" ]; then
      RUNNER_ARGS_RAW='["-ngl","all","--no-ui","--metrics"]'
    fi
    ;;
  0|false|FALSE|no|NO)
    ;;
  *)
    fail "MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_GPU must be 0/1, true/false, or yes/no"
    ;;
esac

mkdir -p "$OUT_DIR"
CANONICAL_REF="$(canonical_model_ref)" || fail "model pull failed"
existing_count="$(runner_count_for_model "$CANONICAL_REF")"
if [ "$existing_count" -ne 0 ]; then
  skip "model $CANONICAL_REF already has $existing_count active runner(s); stop them before running llama.cpp mediation"
fi

start_pinned_runner
assert_single_runner_reused

RUNNER_ENV_FILE="$OUT_DIR/runner-env.env"
runner_env_args >"$RUNNER_ENV_FILE"

case "$PRESSURE" in
  1|true|TRUE|yes|YES|required)
    MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE=1 \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_LABEL="llama.cpp" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_CASE_PREFIX="mml" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_OUT_DIR="$OUT_DIR" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_OWN_OUT_DIR=0 \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_STATE_DIR="$STATE_DIR" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_IMAGE="$IMAGE" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_MODEL_REF="$MODEL_REF" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_ENV_FILE="$RUNNER_ENV_FILE" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_PID="$RUNNER_PID" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_PORT="$RUNNER_PORT" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_ENGINE="llama.cpp" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_REQUEST_MODEL="$REQUEST_MODEL" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_WORKSPACES="$PRESSURE_WORKSPACES" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_CONCURRENCY="$PRESSURE_CONCURRENCY" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_CASES="$PRESSURE_CASES" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_SAMPLES="$PRESSURE_SAMPLES" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_WARMUPS="$PRESSURE_WARMUPS" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_CHAT_TOKENS="$PRESSURE_CHAT_TOKENS" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_STREAM_TOKENS="$PRESSURE_STREAM_TOKENS" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_TELEMETRY="$PRESSURE_TELEMETRY" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_TELEMETRY_INTERVAL="$TELEMETRY_INTERVAL" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_TELEMETRY_ENDPOINTS="$TELEMETRY_ENDPOINTS" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_GATE_MODE="$PRESSURE_GATE_MODE" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_MAX_MODELS_TOTAL_P95_DELTA_MS="$MAX_MODELS_TOTAL_P95_DELTA_MS" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_MAX_CHAT_TOTAL_P95_DELTA_MS="$MAX_CHAT_TOTAL_P95_DELTA_MS" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_MAX_STREAM_TTFB_P95_DELTA_MS="$MAX_STREAM_TTFB_P95_DELTA_MS" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_MAX_DECISION_P95_MS="$MAX_DECISION_P95_MS" \
      "$ROOT/scripts/dev/microagent-e2e-model-mediation-pressure.sh"
    ;;
  0|false|FALSE|no|NO|'')
    MICROAGENT_E2E_MODEL_MEDIATION_RUNNER=1 \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_LABEL="llama.cpp" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_CASE_PREFIX="mml" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_OUT_DIR="$OUT_DIR" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_OWN_OUT_DIR=0 \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_STATE_DIR="$STATE_DIR" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_IMAGE="$IMAGE" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_MODEL_REF="$MODEL_REF" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_ENV_FILE="$RUNNER_ENV_FILE" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_PID="$RUNNER_PID" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_PORT="$RUNNER_PORT" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_ENGINE="llama.cpp" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_REQUEST_MODEL="$REQUEST_MODEL" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_CHAT_TOKENS="$CHAT_TOKENS" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_STREAM_TOKENS="$STREAM_TOKENS" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_SAMPLES="$SAMPLES" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_TELEMETRY="$TELEMETRY" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_TELEMETRY_INTERVAL="$TELEMETRY_INTERVAL" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_TELEMETRY_ENDPOINTS="$TELEMETRY_ENDPOINTS" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_GATE_MODE="$GATE_MODE" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_MAX_MODELS_TOTAL_P95_DELTA_MS="$MAX_MODELS_TOTAL_P95_DELTA_MS" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_MAX_CHAT_TOTAL_P95_DELTA_MS="$MAX_CHAT_TOTAL_P95_DELTA_MS" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_MAX_STREAM_TTFB_P95_DELTA_MS="$MAX_STREAM_TTFB_P95_DELTA_MS" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_MAX_DECISION_P95_MS="$MAX_DECISION_P95_MS" \
      "$ROOT/scripts/dev/microagent-e2e-model-mediation-runner.sh"
    ;;
  *)
    fail "MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_PRESSURE must be 0/1, true/false, or yes/no"
    ;;
esac
