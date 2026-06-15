#!/usr/bin/env bash
#
# microagent-host-worker-vllm-smoke.sh - validate the host-worker path with vLLM.
#
# This script treats vLLM as an external OpenAI-compatible runner. It does not
# install vLLM and does not add vLLM as a microagent dependency.
#
# Runner discovery:
#   1. MICROAGENT_HOST_WORKER_VLLM_BIN
#   2. vllm on PATH
#   3. MICROAGENT_HOST_WORKER_VLLM_REPO/.venv/bin/python
#   4. ../vllm/.venv/bin/python from this workspace
#
# Optional:
#   MICROAGENT_HOST_WORKER_VLLM_MODEL       default: Qwen/Qwen2.5-0.5B-Instruct
#   MICROAGENT_HOST_WORKER_VLLM_HOST        default: 127.0.0.1
#   MICROAGENT_HOST_WORKER_VLLM_PORT        default: auto
#   MICROAGENT_HOST_WORKER_VLLM_OUT_DIR     default: /tmp/microagent-host-worker-vllm-smoke-...
#   MICROAGENT_HOST_WORKER_VLLM_ARGS        JSON string array or shell words
#   MICROAGENT_HOST_WORKER_VLLM_CUDA_HOME   CUDA toolkit path for FlashInfer JIT
#   MICROAGENT_HOST_WORKER_VLLM_FLASHINFER_SAMPLER
#                                           default: 0 for portable smoke runs
#   MICROAGENT_HOST_WORKER_VLLM_PROBE       run Firecracker probe: 0/1 (default: 1)
#   MICROAGENT_HOST_WORKER_VLLM_STARTUP_TIMEOUT
#                                           seconds to wait for /v1/models (default: 300)
#   MICROAGENT_FIRECRACKER                  Firecracker binary for probe runs
#   MICROAGENT_HOST_WORKER_PROBE_*          forwarded to the VM probe
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKSPACE_ROOT="$(cd "$ROOT/.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

MODEL="${MICROAGENT_HOST_WORKER_VLLM_MODEL:-Qwen/Qwen2.5-0.5B-Instruct}"
HOST="${MICROAGENT_HOST_WORKER_VLLM_HOST:-127.0.0.1}"
PORT="${MICROAGENT_HOST_WORKER_VLLM_PORT:-}"
OUT_DIR="${MICROAGENT_HOST_WORKER_VLLM_OUT_DIR:-/tmp/microagent-host-worker-vllm-smoke-$(date +%Y%m%d-%H%M%S)}"
EXTRA_ARGS_RAW="${MICROAGENT_HOST_WORKER_VLLM_ARGS:-}"
RUN_PROBE="${MICROAGENT_HOST_WORKER_VLLM_PROBE:-1}"
STARTUP_TIMEOUT="${MICROAGENT_HOST_WORKER_VLLM_STARTUP_TIMEOUT:-300}"
VLLM_BIN="${MICROAGENT_HOST_WORKER_VLLM_BIN:-}"
VLLM_REPO="${MICROAGENT_HOST_WORKER_VLLM_REPO:-$WORKSPACE_ROOT/vllm}"
VLLM_CUDA_HOME="${MICROAGENT_HOST_WORKER_VLLM_CUDA_HOME:-}"
VLLM_FLASHINFER_SAMPLER="${MICROAGENT_HOST_WORKER_VLLM_FLASHINFER_SAMPLER:-0}"
BASE_URL=""
VLLM_PID=""

skip() { e2e_skip "microagent-host-worker-vllm-smoke: $1"; }
fail() { echo "FAIL microagent-host-worker-vllm-smoke: $1" >&2; exit 1; }

cleanup() {
  local status=$?
  set +e
  if [ -n "$VLLM_PID" ]; then
    kill "$VLLM_PID" >/dev/null 2>&1 || true
    wait "$VLLM_PID" >/dev/null 2>&1 || true
    VLLM_PID=""
  fi
  exit "$status"
}
trap cleanup EXIT

choose_port() {
  python3 - "$HOST" <<'PY'
import socket
import sys

host = sys.argv[1]
with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
    sock.bind((host, 0))
    print(sock.getsockname()[1])
PY
}

parse_extra_args() {
  python3 - "$EXTRA_ARGS_RAW" <<'PY'
import json
import shlex
import sys

raw = sys.argv[1].strip()
if not raw:
    raise SystemExit(0)
try:
    args = json.loads(raw)
except json.JSONDecodeError:
    args = shlex.split(raw)
if not isinstance(args, list) or not all(isinstance(item, str) for item in args):
    raise SystemExit("MICROAGENT_HOST_WORKER_VLLM_ARGS must be JSON string array or shell words")
for item in args:
    print(item)
PY
}

resolve_vllm_command() {
  if [ -n "$VLLM_BIN" ]; then
    [ -x "$VLLM_BIN" ] || skip "MICROAGENT_HOST_WORKER_VLLM_BIN is not executable: $VLLM_BIN"
    VLLM_COMMAND_KIND=bin
    VLLM_COMMAND=("$VLLM_BIN" serve "$MODEL")
    return
  fi
  if command -v vllm >/dev/null 2>&1; then
    VLLM_COMMAND_KIND=bin
    VLLM_COMMAND=("$(command -v vllm)" serve "$MODEL")
    return
  fi
  if [ -x "$VLLM_REPO/.venv/bin/python" ]; then
    VLLM_COMMAND_KIND=repo-venv
    VLLM_COMMAND=("$VLLM_REPO/.venv/bin/python" -m vllm.entrypoints.openai.api_server --model "$MODEL")
    return
  fi
  skip "vLLM is not installed; set MICROAGENT_HOST_WORKER_VLLM_BIN or create $VLLM_REPO/.venv"
}

configure_vllm_cuda_toolkit() {
  local candidate

  if [ -n "$VLLM_CUDA_HOME" ]; then
    [ -x "$VLLM_CUDA_HOME/bin/nvcc" ] || skip "MICROAGENT_HOST_WORKER_VLLM_CUDA_HOME does not contain executable bin/nvcc: $VLLM_CUDA_HOME"
    export CUDA_HOME="$VLLM_CUDA_HOME"
    export PATH="$CUDA_HOME/bin:$PATH"
    return
  fi

  if [ -x "$VLLM_REPO/.venv/bin/python" ]; then
    candidate="$("$VLLM_REPO/.venv/bin/python" <<'PY'
import site
from pathlib import Path

for base in site.getsitepackages():
    path = Path(base) / "nvidia" / "cu13"
    if (path / "bin" / "nvcc").exists():
        print(path)
        raise SystemExit(0)
raise SystemExit(1)
PY
)"
    if [ -n "$candidate" ] && [ -x "$candidate/bin/nvcc" ]; then
      export CUDA_HOME="$candidate"
      export PATH="$CUDA_HOME/bin:$PATH"
      return
    fi
  fi
}

wait_for_vllm() {
  local deadline=$((SECONDS + STARTUP_TIMEOUT))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if python3 - "$BASE_URL/models" <<'PY' >/dev/null 2>&1
import json
import sys
import urllib.request

with urllib.request.urlopen(sys.argv[1], timeout=2) as response:
    doc = json.loads(response.read(1024 * 1024))
    if 200 <= response.status < 300 and isinstance(doc.get("data"), list):
        raise SystemExit(0)
raise SystemExit(1)
PY
    then
      return
    fi
    if ! kill -0 "$VLLM_PID" >/dev/null 2>&1; then
      [ ! -s "$OUT_DIR/vllm.stderr.log" ] || sed -n '1,200p' "$OUT_DIR/vllm.stderr.log" >&2
      [ ! -s "$OUT_DIR/vllm.stdout.log" ] || sed -n '1,120p' "$OUT_DIR/vllm.stdout.log" >&2
      fail "vLLM exited before /v1/models became ready"
    fi
    sleep 2
  done
  [ ! -s "$OUT_DIR/vllm.stderr.log" ] || sed -n '1,200p' "$OUT_DIR/vllm.stderr.log" >&2
  fail "vLLM did not become ready at $BASE_URL/models"
}

case "$RUN_PROBE" in
  1|true|TRUE|yes|YES)
    RUN_PROBE=1
    ;;
  0|false|FALSE|no|NO)
    RUN_PROBE=0
    ;;
  *)
    fail "MICROAGENT_HOST_WORKER_VLLM_PROBE must be 0/1, true/false, or yes/no"
    ;;
esac
case "$STARTUP_TIMEOUT" in
  ''|*[!0-9]*) fail "MICROAGENT_HOST_WORKER_VLLM_STARTUP_TIMEOUT must be a positive integer" ;;
esac
if [ "$STARTUP_TIMEOUT" -le 0 ]; then
  fail "MICROAGENT_HOST_WORKER_VLLM_STARTUP_TIMEOUT must be > 0"
fi

mkdir -p "$OUT_DIR"
if [ -z "$PORT" ]; then
  PORT="$(choose_port)"
fi
BASE_URL="http://$HOST:$PORT/v1"
resolve_vllm_command
configure_vllm_cuda_toolkit
export VLLM_USE_FLASHINFER_SAMPLER="$VLLM_FLASHINFER_SAMPLER"
mapfile -t EXTRA_ARGS < <(parse_extra_args)
if [ "${#EXTRA_ARGS[@]}" -eq 0 ]; then
  EXTRA_ARGS=(--max-model-len 2048 --gpu-memory-utilization 0.70 --enforce-eager)
fi

printf '%s\n' "${VLLM_COMMAND[@]}" "${EXTRA_ARGS[@]}" >"$OUT_DIR/vllm.argv"
echo "microagent-host-worker-vllm-smoke: starting vLLM kind=$VLLM_COMMAND_KIND model=$MODEL url=$BASE_URL cuda_home=${CUDA_HOME:-none} flashinfer_sampler=$VLLM_USE_FLASHINFER_SAMPLER"
"${VLLM_COMMAND[@]}" \
  --host "$HOST" \
  --port "$PORT" \
  "${EXTRA_ARGS[@]}" >"$OUT_DIR/vllm.stdout.log" 2>"$OUT_DIR/vllm.stderr.log" &
VLLM_PID="$!"
wait_for_vllm

echo "microagent-host-worker-vllm-smoke: running host-worker conformance"
python3 "$ROOT/scripts/dev/microagent-host-worker-conformance.py" \
  --base-url "$BASE_URL" \
  --request-model "$MODEL" \
  --runner-engine vllm \
  --telemetry-endpoints "${MICROAGENT_HOST_WORKER_PROBE_RUNNER_TELEMETRY_ENDPOINTS:-/metrics,/health}" \
  --report "$OUT_DIR/conformance.json" >/dev/null || fail "vLLM conformance failed"

if [ "$RUN_PROBE" -eq 1 ]; then
  if [ -z "${MICROAGENT_FIRECRACKER:-}" ]; then
    MICROAGENT_FIRECRACKER="$(e2e_resolve_firecracker)" || skip "Firecracker binary not resolved"
    export MICROAGENT_FIRECRACKER
  elif [ ! -x "${MICROAGENT_FIRECRACKER:-/nonexistent}" ]; then
    skip "MICROAGENT_FIRECRACKER not executable: $MICROAGENT_FIRECRACKER"
  fi
  echo "microagent-host-worker-vllm-smoke: running Firecracker host-worker probe"
  set +e
  MICROAGENT_HOST_WORKER_URL="$BASE_URL" \
    MICROAGENT_HOST_WORKER_MODEL="$MODEL" \
    MICROAGENT_HOST_WORKER_RUNNER_ENGINE=vllm \
    MICROAGENT_HOST_WORKER_TELEMETRY_ADAPTER=vllm \
    MICROAGENT_HOST_WORKER_LABEL=vllm-smoke \
    MICROAGENT_HOST_WORKER_PROBE_REPORT="$OUT_DIR/probe.json" \
    MICROAGENT_HOST_WORKER_CONFORMANCE_REPORT="$OUT_DIR/probe.conformance.json" \
    MICROAGENT_HOST_WORKER_PROBE_PRINT_REPORT=0 \
    MICROAGENT_HOST_WORKER_PROBE_SAMPLES="${MICROAGENT_HOST_WORKER_PROBE_SAMPLES:-2}" \
    MICROAGENT_HOST_WORKER_PROBE_WARMUPS="${MICROAGENT_HOST_WORKER_PROBE_WARMUPS:-1}" \
    MICROAGENT_HOST_WORKER_PROBE_CHAT_PROFILE="${MICROAGENT_HOST_WORKER_PROBE_CHAT_PROFILE:-sustained}" \
    MICROAGENT_HOST_WORKER_PROBE_CHAT_TOKENS="${MICROAGENT_HOST_WORKER_PROBE_CHAT_TOKENS:-32}" \
    MICROAGENT_HOST_WORKER_PROBE_STREAM="${MICROAGENT_HOST_WORKER_PROBE_STREAM:-1}" \
    MICROAGENT_HOST_WORKER_PROBE_STREAM_TOKENS="${MICROAGENT_HOST_WORKER_PROBE_STREAM_TOKENS:-64}" \
    MICROAGENT_HOST_WORKER_PROBE_RUNNER_TELEMETRY_ENDPOINTS="${MICROAGENT_HOST_WORKER_PROBE_RUNNER_TELEMETRY_ENDPOINTS:-/metrics,/health}" \
    "$ROOT/scripts/dev/microagent-host-worker-probe.sh" >"$OUT_DIR/probe.log" 2>&1
  status=$?
  set -e
  if [ "$status" -eq "$E2E_SKIP_EXIT" ]; then
    cat "$OUT_DIR/probe.log" >&2
    skip "Firecracker probe skipped"
  fi
  if [ "$status" -ne 0 ]; then
    cat "$OUT_DIR/probe.log" >&2
    fail "Firecracker host-worker probe failed"
  fi
  grep '^microagent-host-worker-probe:' "$OUT_DIR/probe.log" || true
  "$ROOT/scripts/dev/microagent-host-worker-report-summary.py" \
    "$OUT_DIR/probe.json" >"$OUT_DIR/summary.tsv"
  cat "$OUT_DIR/summary.tsv"
  "$ROOT/scripts/dev/microagent-host-worker-report-summary.py" --pressure \
    "$OUT_DIR/probe.json" >"$OUT_DIR/pressure.tsv"
  cat "$OUT_DIR/pressure.tsv"
fi

python3 - "$OUT_DIR/conformance.json" <<'PY'
import json
import sys
from pathlib import Path

doc = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
cap = doc["capabilities"]
sources = ",".join(cap.get("runner_telemetry_sources") or []) or "none"
print(
    "microagent-host-worker-vllm-smoke: "
    f"required_ok={cap.get('required_ok')} "
    f"models={cap.get('model_count')} "
    f"streaming={cap.get('streaming_chat_completions')} "
    f"runner_telemetry={sources}"
)
PY

echo "PASS microagent-host-worker-vllm-smoke: reports written under $OUT_DIR"
