#!/usr/bin/env bash
#
# microagent-e2e-host-worker-gpu.sh - opt-in host GPU worker acceptance check.
#
# This scenario validates the architecture used when a model runner stays on the
# host and one or more microVMs reach it through microagent's mediation bridge.
# It is intentionally opt-in so ordinary E2E runs do not require a GPU, CUDA, or
# a local model runner.
#
# Required:
#   MICROAGENT_E2E_HOST_WORKER_GPU=1
#
# Runner options:
#   MICROAGENT_E2E_HOST_WORKER_URL     existing OpenAI-compatible base URL
#   MICROAGENT_HOST_WORKER_URL         same, accepted for probe consistency
#   MICROAGENT_LLAMA_SERVER            local llama-server path when no URL is set
#
# Optional:
#   MICROAGENT_CLI                     microagent CLI (default: .build/dev/microagent)
#   MICROAGENT_FIRECRACKER             Firecracker binary for Linux runs
#   MICROAGENT_E2E_HOST_WORKER_MODEL_REF
#   MICROAGENT_E2E_HOST_WORKER_SLOTS   local runner slot count (default: 4)
#   MICROAGENT_E2E_HOST_WORKER_OUT_DIR report directory
#   MICROAGENT_E2E_HOST_WORKER_SAMPLES measured samples per case (default: 2)
#   MICROAGENT_E2E_HOST_WORKER_WARMUPS warmups per case (default: 1)
#   MICROAGENT_E2E_HOST_WORKER_MAX_CHAT_DELTA_MS   default: 500
#   MICROAGENT_E2E_HOST_WORKER_MAX_STREAM_DELTA_MS default: 500
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

CLI="${MICROAGENT_CLI:-$(e2e_exe "$ROOT/.build/dev/microagent")}"
MODEL_REF="${MICROAGENT_E2E_HOST_WORKER_MODEL_REF:-Qwen/Qwen2.5-0.5B-Instruct-GGUF/qwen2.5-0.5b-instruct-q4_k_m.gguf}"
SLOTS="${MICROAGENT_E2E_HOST_WORKER_SLOTS:-4}"
SAMPLES="${MICROAGENT_E2E_HOST_WORKER_SAMPLES:-2}"
WARMUPS="${MICROAGENT_E2E_HOST_WORKER_WARMUPS:-1}"
OUT_DIR="${MICROAGENT_E2E_HOST_WORKER_OUT_DIR:-/tmp/microagent-e2e-host-worker-gpu-$(date +%Y%m%d%H%M%S)}"
STATE_DIR="${MICROAGENT_E2E_HOST_WORKER_STATE_DIR:-${MICROAGENT_HOST_WORKER_PROBE_STATE_DIR:-$HOME/.microagent}}"
HOST_WORKER_URL="${MICROAGENT_E2E_HOST_WORKER_URL:-${MICROAGENT_HOST_WORKER_URL:-}}"
HOST_WORKER_RUNNER_ENGINE="${MICROAGENT_HOST_WORKER_RUNNER_ENGINE:-}"
MAX_CHAT_DELTA_MS="${MICROAGENT_E2E_HOST_WORKER_MAX_CHAT_DELTA_MS:-500}"
MAX_STREAM_DELTA_MS="${MICROAGENT_E2E_HOST_WORKER_MAX_STREAM_DELTA_MS:-500}"
GPU_PATTERN="${MICROAGENT_E2E_HOST_WORKER_GPU_PATTERN:-CUDA[0-9]*[[:space:]]*:|ggml_cuda|CUDA[[:space:]]*:|Metal[[:space:]]*:|Vulkan[[:space:]]*:|SYCL}"
STARTED_RUNNER=0

skip() { e2e_skip "microagent-e2e-host-worker-gpu: $1"; }
fail() { echo "FAIL microagent-e2e-host-worker-gpu: $1" >&2; exit 1; }

cleanup() {
  status="$?"
  set +e
  if [ "$STARTED_RUNNER" -eq 1 ]; then
    "$CLI" model stop "$MODEL_REF" --state-dir "$STATE_DIR" >/dev/null 2>&1
  fi
  exit "$status"
}
trap cleanup EXIT

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

runner_base_url() {
  local runner_json="$1"
  python3 - "$runner_json" <<'PY'
import json
import sys

runner = json.loads(sys.argv[1])
print(f"http://{runner['host']}:{runner['port']}/v1")
PY
}

case "${MICROAGENT_E2E_HOST_WORKER_GPU:-0}" in
  1|true|TRUE|yes|YES|required)
    ;;
  *)
    skip "set MICROAGENT_E2E_HOST_WORKER_GPU=1 to run the opt-in host GPU worker acceptance scenario"
    ;;
esac

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    skip "host GPU worker acceptance currently targets Linux amd64 Firecracker hosts"
    ;;
esac
if [ ! -e /dev/kvm ]; then
  skip "/dev/kvm not available"
fi
if [ ! -x "$CLI" ]; then
  skip "CLI not found at $CLI (run scripts/dev/build-local.sh)"
fi
if [ -z "${MICROAGENT_FIRECRACKER:-}" ]; then
  MICROAGENT_FIRECRACKER="$(e2e_resolve_firecracker)" || skip "Firecracker binary not resolved"
  export MICROAGENT_FIRECRACKER
fi

for value_name in SLOTS SAMPLES WARMUPS; do
  value="${!value_name}"
  case "$value" in
    ''|*[!0-9]*)
      fail "$value_name must be a non-negative integer"
      ;;
  esac
done
if [ "$SLOTS" -le 0 ] || [ "$SAMPLES" -le 0 ]; then
  fail "SLOTS and SAMPLES must be > 0"
fi

mkdir -p "$OUT_DIR"

if [ -z "$HOST_WORKER_URL" ]; then
  if [ -z "${MICROAGENT_LLAMA_SERVER:-}" ] || [ ! -x "${MICROAGENT_LLAMA_SERVER:-/nonexistent}" ]; then
    skip "set MICROAGENT_E2E_HOST_WORKER_URL or executable MICROAGENT_LLAMA_SERVER"
  fi
  if ! "$MICROAGENT_LLAMA_SERVER" --list-devices 2>/dev/null | grep -Eiq "$GPU_PATTERN"; then
    skip "$MICROAGENT_LLAMA_SERVER did not report a GPU matching: $GPU_PATTERN"
  fi
  CANONICAL_MODEL_REF="$(canonical_model_ref)" || fail "model pull failed"
  existing_count="$(runner_count_for_model "$CANONICAL_MODEL_REF")"
  if [ "$existing_count" -ne 0 ]; then
    skip "model $CANONICAL_MODEL_REF already has $existing_count active runner(s); stop them before running acceptance"
  fi
  export MICROAGENT_MODEL_RUNNER_ARGS
  if [ -z "${MICROAGENT_MODEL_RUNNER_ARGS:-}" ]; then
    MICROAGENT_MODEL_RUNNER_ARGS="$(printf '["-ngl","all","--no-ui","--metrics","--slots","--parallel","%s"]' "$SLOTS")"
  fi
  echo "microagent-e2e-host-worker-gpu: starting local GPU runner slots=$SLOTS"
  runner_json="$("$CLI" --json model serve "$MODEL_REF" --state-dir "$STATE_DIR")" || fail "model serve failed"
  STARTED_RUNNER=1
  HOST_WORKER_URL="$(runner_base_url "$runner_json")"
  HOST_WORKER_RUNNER_ENGINE="${HOST_WORKER_RUNNER_ENGINE:-llama.cpp}"
else
  echo "microagent-e2e-host-worker-gpu: using external host worker at $HOST_WORKER_URL"
fi

run_case() {
  local label="$1"
  local workspaces="$2"
  local concurrency="$3"
  local report="$OUT_DIR/$label.json"
  local log="$OUT_DIR/$label.log"

  echo "microagent-e2e-host-worker-gpu: case=$label workspaces=$workspaces per_workspace_concurrency=$concurrency"
  if ! MICROAGENT_CLI="$CLI" \
    MICROAGENT_FIRECRACKER="$MICROAGENT_FIRECRACKER" \
    MICROAGENT_HOST_WORKER_URL="$HOST_WORKER_URL" \
    MICROAGENT_HOST_WORKER_SLOTS="$SLOTS" \
    MICROAGENT_HOST_WORKER_RUNNER_ENGINE="$HOST_WORKER_RUNNER_ENGINE" \
    MICROAGENT_HOST_WORKER_LABEL="$label" \
    MICROAGENT_HOST_WORKER_PROBE_STATE_DIR="$STATE_DIR" \
    MICROAGENT_HOST_WORKER_PROBE_REPORT="$report" \
    MICROAGENT_HOST_WORKER_PROBE_KEEP_FAILED="${MICROAGENT_HOST_WORKER_PROBE_KEEP_FAILED:-1}" \
    MICROAGENT_HOST_WORKER_PROBE_WORKSPACES="$workspaces" \
    MICROAGENT_HOST_WORKER_PROBE_CONCURRENCY="$concurrency" \
    MICROAGENT_HOST_WORKER_PROBE_SAMPLES="$SAMPLES" \
    MICROAGENT_HOST_WORKER_PROBE_WARMUPS="$WARMUPS" \
    MICROAGENT_HOST_WORKER_PROBE_CHAT_PROFILE=sustained \
    MICROAGENT_HOST_WORKER_PROBE_CHAT_TOKENS=48 \
    MICROAGENT_HOST_WORKER_PROBE_STREAM=1 \
    MICROAGENT_HOST_WORKER_PROBE_STREAM_TOKENS=96 \
    MICROAGENT_HOST_WORKER_PROBE_TELEMETRY_INTERVAL=0.1 \
    "$ROOT/scripts/dev/microagent-host-worker-probe.sh" >"$log" 2>&1; then
    cat "$log" >&2
    fail "case $label failed"
  fi
  grep '^microagent-host-worker-probe: workspaces=' "$log" || true
}

run_case "acceptance-ws1-c1" 1 1
run_case "acceptance-ws2-c2" 2 2

"$ROOT/scripts/dev/microagent-host-worker-report-summary.py" \
  "$OUT_DIR/acceptance-ws1-c1.json" \
  "$OUT_DIR/acceptance-ws2-c2.json" >"$OUT_DIR/summary.tsv"
cat "$OUT_DIR/summary.tsv"
"$ROOT/scripts/dev/microagent-host-worker-report-summary.py" --pressure \
  "$OUT_DIR/acceptance-ws1-c1.json" \
  "$OUT_DIR/acceptance-ws2-c2.json" >"$OUT_DIR/pressure.tsv"
cat "$OUT_DIR/pressure.tsv"

python3 - "$MAX_CHAT_DELTA_MS" "$MAX_STREAM_DELTA_MS" "$OUT_DIR/acceptance-ws1-c1.json" "$OUT_DIR/acceptance-ws2-c2.json" <<'PY'
import json
import sys
from pathlib import Path

max_chat = float(sys.argv[1])
max_stream = float(sys.argv[2])
expectations = {
    "acceptance-ws1-c1": {"workspace_count": 1, "level": "1", "effective": 1},
    "acceptance-ws2-c2": {"workspace_count": 2, "level": "2", "effective": 4},
}

for raw_path in sys.argv[3:]:
    path = Path(raw_path)
    doc = json.loads(path.read_text(encoding="utf-8"))
    name = path.stem
    expected = expectations[name]
    if doc.get("workspace_count") != expected["workspace_count"]:
        raise SystemExit(f"{name}: workspace_count mismatch: {doc.get('workspace_count')}")
    level = expected["level"]
    level_doc = (doc.get("matrix") or {}).get(level) or {}
    for endpoint in ("chat", "stream"):
        if endpoint not in (level_doc.get("guest") or {}):
            raise SystemExit(f"{name}: missing guest endpoint {endpoint}")
        effective = level_doc["guest"][endpoint].get("concurrency")
        if effective != expected["effective"]:
            raise SystemExit(f"{name}: effective concurrency mismatch for {endpoint}: {effective}")
    overhead = level_doc.get("overhead") or {}
    chat_delta = float(overhead["chat"]["delta_ms"])
    stream_delta = float(overhead["stream"]["delta_ms"])
    stream = overhead["stream"]
    for required in ("body_read_delta_ms", "body_read_per_chunk_gap_delta_ms", "ttfb_delta_ms"):
        if required not in stream:
            raise SystemExit(f"{name}: missing stream metric {required}")
    if chat_delta > max_chat:
        raise SystemExit(f"{name}: chat delta {chat_delta:.3f}ms exceeds {max_chat:.3f}ms")
    if stream_delta > max_stream:
        raise SystemExit(f"{name}: stream delta {stream_delta:.3f}ms exceeds {max_stream:.3f}ms")
    print(
        f"ACCEPT {name}: chat_delta={chat_delta:.3f}ms "
        f"stream_delta={stream_delta:.3f}ms "
        f"stream_ttfb_delta={stream['ttfb_delta_ms']:.3f}ms "
        f"stream_body_delta={stream['body_read_delta_ms']:.3f}ms "
        f"chunk_gap_delta={stream['body_read_per_chunk_gap_delta_ms']:.3f}ms"
    )
PY

echo "PASS microagent-e2e-host-worker-gpu: acceptance reports written under $OUT_DIR"
