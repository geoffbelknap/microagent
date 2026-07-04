#!/usr/bin/env bash
#
# microagent-e2e-model-mediation-vllm.sh - opt-in production model mediation
# check against a real vLLM OpenAI-compatible GPU runner.
#
# This validates the real `run --model` path with the experimental host-worker
# mediator enabled, while using microagent's runner command abstraction instead
# of llama.cpp. A small fabricated GGUF model-store record satisfies
# microagent's model pairing contract; the custom runner command ignores that
# path and starts vLLM with a Hugging Face model id.
#
# Required:
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM=1
#
# Optional:
#   MICROAGENT_CLI                                microagent CLI
#   MICROAGENT_FIRECRACKER                        Firecracker binary
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM_OUT_DIR   output dir
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM_STATE_DIR state dir
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM_KEEP      preserve reports/state
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM_KEEP_STATE
#                                                    preserve workspaces on fail
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM_IMAGE     guest image with sh + curl
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM_REPO      vLLM repo checkout
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM_PYTHON    vLLM venv python
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM_MODEL     HF model id
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM_ARGS      vLLM extra args
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM_CUDA_HOME CUDA toolkit path
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM_FLASHINFER_SAMPLER
#                                                    default: 0
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM_CHAT_TOKENS
#                                                    default: 32
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM_STREAM_TOKENS
#                                                    default: 64
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM_SAMPLES     default: 3
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM_TELEMETRY   off, auto, or required (default: auto)
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM_GATE_MODE   off, warn, or required (default: required)
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM_PRESSURE    0/1 (default: 0)
#   MICROAGENT_E2E_MODEL_MEDIATION_VLLM_PRESSURE_PRESET
#                                                    default, baseline, ci, or hardware
#
# The vLLM fixture does not need to be a source checkout. Any directory whose
# .venv has the vllm package installed works, e.g.:
#   uv venv .venv && uv pip install --python .venv/bin/python vllm
# MICROAGENT_E2E_MODEL_MEDIATION_VLLM_REPO is only used as the runner's
# working directory and to derive the default .venv/bin/python path; set
# MICROAGENT_E2E_MODEL_MEDIATION_VLLM_PYTHON to use an interpreter elsewhere.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"
# shellcheck source=scripts/dev/microagent-model-mediation-pressure-presets.sh disable=SC1091
. "$ROOT/scripts/dev/microagent-model-mediation-pressure-presets.sh"

CLI="${MICROAGENT_CLI:-$(e2e_exe "$ROOT/.build/dev/microagent")}"
OUT_DIR="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_OUT_DIR:-/tmp/microagent-e2e-model-mediation-vllm-$(date +%Y%m%d%H%M%S)}"
STATE_DIR="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_STATE_DIR:-$OUT_DIR/state}"
KEEP_FAILED="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_KEEP:-${MICROAGENT_KEEP_MICROAGENT_E2E_MODEL_MEDIATION_VLLM:-0}}"
KEEP_STATE="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_KEEP_STATE:-0}"
IMAGE="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_IMAGE:-quay.io/curl/curl:latest}"
MODEL_REF="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_MODEL_REF:-stub/stub-model-GGUF/stub.gguf}"
CANONICAL_REF="hf.co/stub/stub-model-GGUF@main/stub.gguf"
VLLM_REPO="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_REPO:-$ROOT/../vllm}"
VLLM_PYTHON="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_PYTHON:-$VLLM_REPO/.venv/bin/python}"
VLLM_MODEL="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_MODEL:-Qwen/Qwen2.5-0.5B-Instruct}"
VLLM_SERVED_MODEL="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_SERVED_MODEL_NAME:-$VLLM_MODEL}"
VLLM_ARGS="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_ARGS:---max-model-len 2048 --gpu-memory-utilization 0.70 --enforce-eager --dtype half}"
VLLM_HEALTH_PATH="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_HEALTH_PATH:-/health}"
VLLM_CUDA_HOME="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_CUDA_HOME:-}"
VLLM_FLASHINFER_SAMPLER="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_FLASHINFER_SAMPLER:-0}"
CHAT_TOKENS="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_CHAT_TOKENS:-32}"
STREAM_TOKENS="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_STREAM_TOKENS:-64}"
SAMPLES="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_SAMPLES:-3}"
TELEMETRY="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_TELEMETRY:-auto}"
TELEMETRY_INTERVAL="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_TELEMETRY_INTERVAL:-0.5}"
TELEMETRY_ENDPOINTS="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_TELEMETRY_ENDPOINTS:-/metrics,/health}"
GATE_MODE="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_GATE_MODE:-required}"
PRESSURE="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_PRESSURE:-0}"
PRESSURE_PRESET="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_PRESSURE_PRESET:-default}"
case "$PRESSURE" in
  1|true|TRUE|yes|YES|required) ;;
  *) PRESSURE_PRESET=default ;;
esac
PRESSURE_PREFIX="MICROAGENT_E2E_MODEL_MEDIATION_VLLM"
PRESSURE_WORKSPACES="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_WORKSPACES 2)"
PRESSURE_CONCURRENCY="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_CONCURRENCY 1,2)"
PRESSURE_CASES="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_CASES direct,local,pf,pa)"
PRESSURE_SAMPLES="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_SAMPLES "$SAMPLES")"
PRESSURE_WARMUPS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_WARMUPS 1)"
PRESSURE_CHAT_TOKENS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_CHAT_TOKENS "$CHAT_TOKENS")"
PRESSURE_STREAM_TOKENS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_STREAM_TOKENS "$STREAM_TOKENS")"
PRESSURE_GATE_MODE="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_GATE_MODE warn)"
PRESSURE_TELEMETRY="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_TELEMETRY "$TELEMETRY")"
MAX_MODELS_TOTAL_P95_DELTA_MS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_MAX_MODELS_TOTAL_P95_DELTA_MS "${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_MAX_MODELS_TOTAL_P95_DELTA_MS:-100}")"
MAX_CHAT_TOTAL_P95_DELTA_MS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_MAX_CHAT_TOTAL_P95_DELTA_MS "${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_MAX_CHAT_TOTAL_P95_DELTA_MS:-500}")"
MAX_STREAM_TTFB_P95_DELTA_MS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_MAX_STREAM_TTFB_P95_DELTA_MS "${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_MAX_STREAM_TTFB_P95_DELTA_MS:-250}")"
MAX_DECISION_P95_MS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_MAX_DECISION_P95_MS "${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_MAX_DECISION_P95_MS:-100}")"
RUNNER_COMMAND_JSON=""
RUNNER_ENV_JSON=""
RUNNER_PID=""
RUNNER_PORT=""
RESOLVED_CUDA_HOME=""
CTRL_FLAGS=(--backend linux-kvm --state-dir "$STATE_DIR")
case "$KEEP_STATE" in
  1|true|TRUE|yes|YES)
    KEEP_STATE=1
    ;;
  *)
    KEEP_STATE=0
    ;;
esac

skip() { e2e_skip "microagent-e2e-model-mediation-vllm: $1"; }
fail() { echo "FAIL microagent-e2e-model-mediation-vllm: $1" >&2; exit 1; }

cleanup() {
  local status=$?
  set +e
  if [ "$KEEP_STATE" = "1" ] && [ "$status" -ne 0 ]; then
    echo "microagent-e2e-model-mediation-vllm: preserved workspace state under $STATE_DIR" >&2
  else
    for workspace in mmv-direct mmv-local mmv-pa mmv-pd mmv-pf mmv-pfd mmv-pu; do
      "$CLI" kill "$workspace" "${CTRL_FLAGS[@]}" >/dev/null 2>&1 || true
      "$CLI" delete "$workspace" --force --yes "${CTRL_FLAGS[@]}" >/dev/null 2>&1 || true
    done
  fi
  "$CLI" model stop "$CANONICAL_REF" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  if [ "$KEEP_FAILED" = "1" ]; then
    if [ "$status" -ne 0 ]; then
      echo "microagent-e2e-model-mediation-vllm: preserved failed state under $OUT_DIR" >&2
    else
      echo "microagent-e2e-model-mediation-vllm: preserved reports under $OUT_DIR" >&2
    fi
  else
    rm -rf "$OUT_DIR"
  fi
  exit "$status"
}
trap cleanup EXIT

case "${MICROAGENT_E2E_MODEL_MEDIATION_VLLM:-0}" in
  1|true|TRUE|yes|YES|required)
    ;;
  *)
    skip "set MICROAGENT_E2E_MODEL_MEDIATION_VLLM=1 to run the opt-in vLLM model mediation scenario"
    ;;
esac
case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    skip "vLLM model mediation E2E currently targets the Linux host backend"
    ;;
esac
if [ ! -x "$CLI" ]; then
  skip "CLI not found at $CLI (run scripts/dev/build-local.sh)"
fi
if [ ! -e /dev/kvm ]; then
  skip "/dev/kvm not available"
fi
if ! pressure_preset_validate "$PRESSURE_PRESET"; then
  fail "MICROAGENT_E2E_MODEL_MEDIATION_VLLM_PRESSURE_PRESET must be default, baseline, ci, or hardware"
fi
if [ -z "${MICROAGENT_FIRECRACKER:-}" ]; then
  MICROAGENT_FIRECRACKER="$(e2e_resolve_firecracker)" || skip "Firecracker binary not resolved"
  export MICROAGENT_FIRECRACKER
fi
if [ ! -d "$VLLM_REPO" ]; then
  skip "vLLM repo not found at $VLLM_REPO"
fi
if [ ! -x "$VLLM_PYTHON" ]; then
  skip "vLLM python not executable at $VLLM_PYTHON"
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
  *) fail "MICROAGENT_E2E_MODEL_MEDIATION_VLLM_TELEMETRY must be off, auto, or required" ;;
esac
case "$GATE_MODE" in
  off|warn|required) ;;
  *) fail "MICROAGENT_E2E_MODEL_MEDIATION_VLLM_GATE_MODE must be off, warn, or required" ;;
esac

mkdir -p "$OUT_DIR/bin" "$STATE_DIR/models/blobs"

check_vllm_gpu() {
  local stdout="$OUT_DIR/vllm-gpu-check.stdout"
  local stderr="$OUT_DIR/vllm-gpu-check.stderr"
  if (cd "$VLLM_REPO" && "$VLLM_PYTHON" - >"$stdout" 2>"$stderr" <<'PY')
import sys
import torch
import vllm

if not torch.cuda.is_available():
    raise SystemExit("torch cuda is not available")
if torch.cuda.device_count() < 1:
    raise SystemExit("no cuda devices")
props = torch.cuda.get_device_properties(0)
print("python", sys.version.split()[0])
print("torch", torch.__version__, "cuda", torch.version.cuda)
print("vllm", getattr(vllm, "__version__", "unknown"))
print("device0", torch.cuda.get_device_name(0), "mem", props.total_memory)
PY
  then
    cat "$stdout"
    return 0
  fi
  if [ "${MICROAGENT_E2E_MODEL_MEDIATION_VLLM:-0}" = "required" ]; then
    cat "$stdout" "$stderr" >&2 || true
    fail "vLLM CUDA preflight failed"
  fi
  skip "vLLM CUDA preflight failed"
}

configure_vllm_cuda_toolkit() {
  local candidate

  if [ -n "$VLLM_CUDA_HOME" ]; then
    [ -x "$VLLM_CUDA_HOME/bin/nvcc" ] || skip "MICROAGENT_E2E_MODEL_MEDIATION_VLLM_CUDA_HOME does not contain executable bin/nvcc: $VLLM_CUDA_HOME"
    RESOLVED_CUDA_HOME="$VLLM_CUDA_HOME"
    return
  fi

  candidate="$("$VLLM_PYTHON" <<'PY' 2>/dev/null || true
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
    RESOLVED_CUDA_HOME="$candidate"
  fi
}

stage_stub_model() {
  local blob="$STATE_DIR/models/blobs/stub.gguf"
  printf '%s' "GGUF-stub" >"$blob"
  python3 - "$STATE_DIR/models/index.json" "$blob" <<'PY'
import json
import sys
import time
from pathlib import Path

index_path = Path(sys.argv[1])
blob = sys.argv[2]
index_path.write_text(
    json.dumps(
        {
            "models": [
                {
                    "model_ref": "hf.co/stub/stub-model-GGUF@main/stub.gguf",
                    "output_path": blob,
                    "size_bytes": 9,
                    "last_used_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                }
            ]
        },
        indent=2,
        sort_keys=True,
    )
    + "\n",
    encoding="utf-8",
)
PY
}

write_vllm_runner() {
  local runner="$OUT_DIR/bin/vllm-runner"
  cat >"$runner" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

host="127.0.0.1"
port=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --model-path)
      shift 2
      ;;
    --host)
      host="$2"
      shift 2
      ;;
    --port)
      port="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

if [ -z "$port" ]; then
  echo "missing --port" >&2
  exit 2
fi

repo="${MICROAGENT_VLLM_REPO:?MICROAGENT_VLLM_REPO is required}"
python="${MICROAGENT_VLLM_PYTHON:?MICROAGENT_VLLM_PYTHON is required}"
model="${MICROAGENT_VLLM_MODEL:?MICROAGENT_VLLM_MODEL is required}"
served="${MICROAGENT_VLLM_SERVED_MODEL_NAME:-$model}"
args_raw="${MICROAGENT_VLLM_ARGS:-}"
extra_args=()
if [ -n "$args_raw" ]; then
  # vLLM args used here are flag/value pairs without embedded whitespace.
  # This is intentionally simple for a dev E2E harness.
  read -r -a extra_args <<<"$args_raw"
fi

cd "$repo"
exec "$python" -m vllm.entrypoints.openai.api_server \
  --model "$model" \
  --served-model-name "$served" \
  --host "$host" \
  --port "$port" \
  "${extra_args[@]}"
EOF
  chmod +x "$runner"
  RUNNER_COMMAND_JSON="$(python3 - "$runner" <<'PY'
import json
import sys

print(json.dumps([sys.argv[1], "--model-path", "{model}", "--host", "{host}", "--port", "{port}"]))
PY
)"
  RUNNER_ENV_JSON="$(python3 - "$VLLM_REPO" "$VLLM_PYTHON" "$VLLM_MODEL" "$VLLM_SERVED_MODEL" "$VLLM_ARGS" "$VLLM_FLASHINFER_SAMPLER" "$RESOLVED_CUDA_HOME" "$PATH" <<'PY'
import json
import sys

keys = [
    "MICROAGENT_VLLM_REPO",
    "MICROAGENT_VLLM_PYTHON",
    "MICROAGENT_VLLM_MODEL",
    "MICROAGENT_VLLM_SERVED_MODEL_NAME",
    "MICROAGENT_VLLM_ARGS",
    "VLLM_USE_FLASHINFER_SAMPLER",
]
values = dict(zip(keys, sys.argv[1:7]))
cuda_home = sys.argv[7]
base_path = sys.argv[8]
if cuda_home:
    values["CUDA_HOME"] = cuda_home
    values["PATH"] = f"{cuda_home}/bin:{base_path}"
print(json.dumps(values))
PY
)"
}

runner_env_args() {
  printf '%s\n' \
    "MICROAGENT_MODEL_RUNNER_COMMAND=$RUNNER_COMMAND_JSON" \
    "MICROAGENT_MODEL_RUNNER_NAME=vllm" \
    "MICROAGENT_MODEL_RUNNER_HEALTH_PATH=$VLLM_HEALTH_PATH" \
    "MICROAGENT_MODEL_RUNNER_ENV=$RUNNER_ENV_JSON" \
    "MICROAGENT_MODEL_RUNNER_ARGS="
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
  echo "microagent-e2e-model-mediation-vllm: pinned vLLM runner pid=$RUNNER_PID"
}

warm_vllm_runner() {
  python3 - "$RUNNER_PORT" "$VLLM_SERVED_MODEL" "$CHAT_TOKENS" <<'PY'
import json
import sys
import urllib.request

port, model, tokens = sys.argv[1:]
payload = {
    "model": model,
    "messages": [{"role": "user", "content": "Reply with exactly PONG."}],
    "max_tokens": int(tokens),
    "temperature": 0,
}
req = urllib.request.Request(
    f"http://127.0.0.1:{port}/v1/chat/completions",
    data=json.dumps(payload).encode("utf-8"),
    headers={"Content-Type": "application/json"},
    method="POST",
)
with urllib.request.urlopen(req, timeout=120) as response:
    body = response.read(1024 * 1024)
    if response.status != 200 or b'"choices"' not in body:
        raise SystemExit(f"warmup failed with HTTP {response.status}")
PY
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
if runner.get("engine") != "vllm":
    raise SystemExit(f"runner engine = {runner.get('engine')!r}, want 'vllm'")
PY
}

check_vllm_gpu
configure_vllm_cuda_toolkit
stage_stub_model
write_vllm_runner
start_pinned_runner
warm_vllm_runner
assert_single_runner_reused

RUNNER_ENV_FILE="$OUT_DIR/runner-env.env"
runner_env_args >"$RUNNER_ENV_FILE"

case "$PRESSURE" in
  1|true|TRUE|yes|YES|required)
    MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE=1 \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_LABEL="vllm" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_CASE_PREFIX="mmv" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_OUT_DIR="$OUT_DIR" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_OWN_OUT_DIR=0 \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_STATE_DIR="$STATE_DIR" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_IMAGE="$IMAGE" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_MODEL_REF="$MODEL_REF" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_ENV_FILE="$RUNNER_ENV_FILE" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_PID="$RUNNER_PID" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_PORT="$RUNNER_PORT" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_ENGINE="vllm" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_REQUEST_MODEL="$VLLM_SERVED_MODEL" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_KEEP_STATE="$KEEP_STATE" \
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
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_LABEL="vllm" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_CASE_PREFIX="mmv" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_OUT_DIR="$OUT_DIR" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_OWN_OUT_DIR=0 \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_STATE_DIR="$STATE_DIR" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_IMAGE="$IMAGE" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_MODEL_REF="$MODEL_REF" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_ENV_FILE="$RUNNER_ENV_FILE" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_PID="$RUNNER_PID" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_PORT="$RUNNER_PORT" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_ENGINE="vllm" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_REQUEST_MODEL="$VLLM_SERVED_MODEL" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_KEEP_STATE="$KEEP_STATE" \
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
    fail "MICROAGENT_E2E_MODEL_MEDIATION_VLLM_PRESSURE must be 0/1, true/false, or yes/no"
    ;;
esac
