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
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

CLI="${MICROAGENT_CLI:-$(e2e_exe "$ROOT/.build/dev/microagent")}"
OUT_DIR="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_OUT_DIR:-/tmp/ma-e2e-mm-vllm-$(date +%Y%m%d%H%M%S)}"
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
MAX_MODELS_TOTAL_P95_DELTA_MS="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_MAX_MODELS_TOTAL_P95_DELTA_MS:-100}"
MAX_CHAT_TOTAL_P95_DELTA_MS="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_MAX_CHAT_TOTAL_P95_DELTA_MS:-500}"
MAX_STREAM_TTFB_P95_DELTA_MS="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_MAX_STREAM_TTFB_P95_DELTA_MS:-250}"
MAX_DECISION_P95_MS="${MICROAGENT_E2E_MODEL_MEDIATION_VLLM_MAX_DECISION_P95_MS:-100}"
POLICY_PID=""
POLICY_URL=""
RUNNER_COMMAND_JSON=""
RUNNER_ENV_JSON=""
RUNNER_PID=""
RUNNER_PORT=""
TELEMETRY_PID=""
TELEMETRY_PHASE_FILE=""
RESOLVED_CUDA_HOME=""
RUN_FLAGS=(--backend firecracker --network isolated --state-dir "$STATE_DIR" --model "$MODEL_REF")
CTRL_FLAGS=(--backend firecracker --state-dir "$STATE_DIR")
case "$KEEP_STATE" in
  1|true|TRUE|yes|YES)
    KEEP_STATE=1
    ;;
  *)
    RUN_FLAGS+=(--rm)
    KEEP_STATE=0
    ;;
esac
RUN_FLAGS+=("$IMAGE")

skip() { e2e_skip "microagent-e2e-model-mediation-vllm: $1"; }
fail() { echo "FAIL microagent-e2e-model-mediation-vllm: $1" >&2; exit 1; }

cleanup() {
  local status=$?
  set +e
  stop_telemetry
  if [ -n "$POLICY_PID" ]; then
    kill "$POLICY_PID" >/dev/null 2>&1 || true
    wait "$POLICY_PID" >/dev/null 2>&1 || true
    POLICY_PID=""
  fi
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

stop_telemetry() {
  if [ -n "$TELEMETRY_PID" ]; then
    kill "$TELEMETRY_PID" >/dev/null 2>&1 || true
    wait "$TELEMETRY_PID" >/dev/null 2>&1 || true
    TELEMETRY_PID=""
  fi
  if [ -n "$TELEMETRY_PHASE_FILE" ]; then
    rm -f "$TELEMETRY_PHASE_FILE"
    TELEMETRY_PHASE_FILE=""
  fi
}

set_telemetry_phase() {
  local phase="$1"
  if [ -n "$TELEMETRY_PHASE_FILE" ]; then
    printf '%s\n' "$phase" >"$TELEMETRY_PHASE_FILE"
  fi
}

start_telemetry() {
  case "$TELEMETRY" in
    off)
      return
      ;;
    auto|required)
      ;;
    *)
      fail "MICROAGENT_E2E_MODEL_MEDIATION_VLLM_TELEMETRY must be off, auto, or required"
      ;;
  esac
  TELEMETRY_PHASE_FILE="$OUT_DIR/telemetry.phase"
  printf '%s\n' startup >"$TELEMETRY_PHASE_FILE"
  python3 "$ROOT/scripts/dev/microagent-model-mediation-telemetry.py" sample \
    --runner-root-url "http://127.0.0.1:$RUNNER_PORT" \
    --phase-file "$TELEMETRY_PHASE_FILE" \
    --runner-out "$OUT_DIR/runner-telemetry.jsonl" \
    --gpu-out "$OUT_DIR/gpu-telemetry.csv" \
    --endpoints "$TELEMETRY_ENDPOINTS" \
    --interval "$TELEMETRY_INTERVAL" \
    --gpu "$TELEMETRY" &
  TELEMETRY_PID="$!"
  sleep 0.2
  if ! kill -0 "$TELEMETRY_PID" >/dev/null 2>&1; then
    wait "$TELEMETRY_PID" >/dev/null 2>&1 || true
    TELEMETRY_PID=""
    fail "telemetry sampler exited before collection"
  fi
  echo "microagent-e2e-model-mediation-vllm: telemetry writing to $OUT_DIR/runner-telemetry.jsonl and $OUT_DIR/gpu-telemetry.csv"
}

write_telemetry_summary() {
  if [ "$TELEMETRY" = "off" ]; then
    return
  fi
  stop_telemetry
  python3 "$ROOT/scripts/dev/microagent-model-mediation-telemetry.py" summary \
    --runner-in "$OUT_DIR/runner-telemetry.jsonl" \
    --gpu-in "$OUT_DIR/gpu-telemetry.csv" \
    --adapter vllm \
    --out "$OUT_DIR/telemetry-summary.tsv"
}

write_gate_summary() {
  python3 "$ROOT/scripts/dev/microagent-model-mediation-telemetry.py" gate \
    --profile-comparison "$OUT_DIR/profile-comparison.tsv" \
    --audit-summary "$OUT_DIR/summary.tsv" \
    --out "$OUT_DIR/mediation-gates.tsv" \
    --mode "$GATE_MODE" \
    --max-models-total-p95-delta-ms "$MAX_MODELS_TOTAL_P95_DELTA_MS" \
    --max-chat-total-p95-delta-ms "$MAX_CHAT_TOTAL_P95_DELTA_MS" \
    --max-stream-ttfb-p95-delta-ms "$MAX_STREAM_TTFB_P95_DELTA_MS" \
    --max-decision-p95-ms "$MAX_DECISION_P95_MS"
}

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

choose_port() {
  python3 - <<'PY'
import socket

with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
}

wait_for_policy() {
  local stdout="$1"
  local deadline=$((SECONDS + 10))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if [ -s "$stdout" ] && grep -q '^ready ' "$stdout"; then
      return 0
    fi
    if [ -n "$POLICY_PID" ] && ! kill -0 "$POLICY_PID" >/dev/null 2>&1; then
      return 1
    fi
    sleep 0.1
  done
  return 1
}

stop_policy() {
  if [ -n "$POLICY_PID" ]; then
    kill "$POLICY_PID" >/dev/null 2>&1 || true
    wait "$POLICY_PID" >/dev/null 2>&1 || true
    POLICY_PID=""
  fi
  POLICY_URL=""
}

start_policy() {
  local decision="$1"
  local label="$2"
  stop_policy
  local stdout="$OUT_DIR/policy-$label.stdout"
  local stderr="$OUT_DIR/policy-$label.stderr"
  local log="$OUT_DIR/policy-$label.jsonl"
  python3 "$ROOT/scripts/dev/microagent-host-worker-policy-stub.py" \
    --decision "$decision" \
    --log-path "$log" \
    --bind-host 127.0.0.1 \
    --bind-port 0 >"$stdout" 2>"$stderr" &
  POLICY_PID=$!
  wait_for_policy "$stdout" || {
    cat "$stdout" "$stderr" >&2 || true
    fail "policy stub did not become ready"
  }
  POLICY_URL="http://$(awk '/^ready / {print $2; exit}' "$stdout")/decision"
}

audit_log_for_workspace() {
  local workspace="$1"
  printf '%s\n' "$STATE_DIR/host-workers/${workspace}_model.openai.jsonl"
}

audit_report_for_workspace() {
  local workspace="$1"
  printf '%s\n' "$OUT_DIR/${workspace}_model.openai.jsonl"
}

reset_audit_log() {
  local workspace="$1"
  rm -f "$(audit_log_for_workspace "$workspace")" "$(audit_report_for_workspace "$workspace")"
}

capture_audit_log() {
  local workspace="$1"
  local live_log
  local report_log
  live_log="$(audit_log_for_workspace "$workspace")"
  report_log="$(audit_report_for_workspace "$workspace")"
  if [ -e "$live_log" ]; then
    cp "$live_log" "$report_log"
  fi
}

assert_index_clean() {
  python3 - "$STATE_DIR/host-workers/index.json" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
if not path.exists():
    raise SystemExit(0)
doc = json.loads(path.read_text(encoding="utf-8") or "{}")
mediators = doc.get("mediators") or []
if mediators:
    raise SystemExit(f"mediators still indexed: {mediators}")
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

summarize_audit() {
  local label="$1"
  local workspace="$2"
  local expected="$3"
  local log_path
  log_path="$(audit_report_for_workspace "$workspace")"
  python3 - "$label" "$expected" "$log_path" "$OUT_DIR/summary.tsv" <<'PY'
import json
import sys
from collections import Counter
from pathlib import Path

label, expected, raw_log, raw_summary = sys.argv[1:]
log = Path(raw_log)
summary = Path(raw_summary)
events = []
if log.exists():
    for line in log.read_text(encoding="utf-8").splitlines():
        if line.strip():
            events.append(json.loads(line))
counter = Counter(str(event.get("event")) for event in events)
results = Counter(str(event.get("mediation_result")) for event in events if event.get("mediation_result") is not None)
durations = [float(event.get("duration_ms")) for event in events if event.get("event") == "request_end" and event.get("duration_ms") is not None]
decision_ms = [float(event.get("mediation_decision_ms")) for event in events if event.get("mediation_decision_ms") is not None]

def p95(values: list[float]) -> str:
    if not values:
        return ""
    values = sorted(values)
    idx = min(len(values) - 1, int((len(values) - 1) * 0.95))
    return f"{values[idx]:.3f}"

if not summary.exists():
    summary.write_text(
        "case\texpected_status\taudit_events\tmediation_results\trequest_end_p95_ms\tdecision_p95_ms\tlog\n",
        encoding="utf-8",
    )
with summary.open("a", encoding="utf-8") as handle:
    handle.write(
        "\t".join(
            [
                label,
                expected,
                ",".join(f"{k}:{v}" for k, v in sorted(counter.items())),
                ",".join(f"{k}:{v}" for k, v in sorted(results.items())),
                p95(durations),
                p95(decision_ms),
                str(log),
            ]
        )
        + "\n"
    )
PY
}

extract_guest_stdout() {
  local run_log="$1"
  local stdout_path="$2"
  python3 - "$run_log" "$stdout_path" <<'PY'
import json
import sys
from pathlib import Path

run_log = Path(sys.argv[1])
stdout_path = Path(sys.argv[2])
text = run_log.read_text(encoding="utf-8", errors="replace")
try:
    doc = json.loads(text)
except json.JSONDecodeError:
    stdout = text
else:
    stdout = (doc.get("result") or {}).get("stdout")
    if stdout is None:
        stdout = ((doc.get("response") or {}).get("result") or {}).get("stdout")
    if stdout is None:
        stdout = text
stdout_path.write_text(stdout, encoding="utf-8")
PY
}

summarize_profiles() {
  local label="$1"
  local expected="$2"
  local stdout_path="$3"
  python3 - "$label" "$expected" "$stdout_path" "$OUT_DIR/profiles.tsv" <<'PY'
import sys
from pathlib import Path

label, expected, raw_stdout, raw_summary = sys.argv[1:]
stdout_path = Path(raw_stdout)
summary = Path(raw_summary)
markers: dict[str, str] = {}
profile_rows: list[list[str]] = []
for line in stdout_path.read_text(encoding="utf-8", errors="replace").splitlines():
    if line.startswith("PROFILE\t"):
        profile_rows.append(line.split("\t"))
        continue
    key, sep, value = line.partition("=")
    if sep and key:
        markers[key.strip()] = value.strip()

def ms(raw: str | None) -> str:
    if raw in (None, ""):
        return ""
    try:
        return f"{float(raw) * 1000:.3f}"
    except ValueError:
        return ""

if not summary.exists():
    summary.write_text(
        "case\tendpoint\tsample\texpected_status\tstatus\ttotal_ms\tttfb_ms\tbytes\tchunks\tstdout\n",
        encoding="utf-8",
    )
with summary.open("a", encoding="utf-8") as handle:
    if profile_rows:
        for row in profile_rows:
            if len(row) != 8:
                raise SystemExit(f"malformed PROFILE row: {row!r}")
            _, endpoint, sample, status, ttfb, total, size, chunks = row
            handle.write(
                "\t".join(
                    [
                        label,
                        endpoint,
                        sample,
                        expected,
                        status,
                        ms(total),
                        ms(ttfb),
                        size,
                        chunks,
                        str(stdout_path),
                    ]
                )
                + "\n"
            )
        raise SystemExit(0)
    for endpoint, prefix in [
        ("models", "MODEL"),
        ("chat", "CHAT"),
        ("stream", "STREAM"),
    ]:
        handle.write(
            "\t".join(
                [
                    label,
                    endpoint,
                    "1",
                    expected,
                    markers.get(f"{prefix}_STATUS", ""),
                    ms(markers.get(f"{prefix}_TOTAL")),
                    ms(markers.get(f"{prefix}_TTFB")),
                    markers.get(f"{prefix}_BYTES", ""),
                    markers.get(f"{prefix}_CHUNKS", ""),
                    str(stdout_path),
                ]
            )
            + "\n"
        )
PY
}

write_profile_summaries() {
  python3 - "$OUT_DIR/profiles.tsv" "$OUT_DIR/profile-summary.tsv" "$OUT_DIR/profile-comparison.tsv" <<'PY'
import csv
import math
import statistics
import sys
from collections import defaultdict
from pathlib import Path

profiles_path = Path(sys.argv[1])
summary_path = Path(sys.argv[2])
comparison_path = Path(sys.argv[3])
rows = list(csv.DictReader(profiles_path.open(encoding="utf-8"), delimiter="\t"))

def as_float(raw: str) -> float | None:
    if raw == "":
        return None
    try:
        return float(raw)
    except ValueError:
        return None

def percentile(values: list[float], pct: float) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    idx = max(0, min(len(ordered) - 1, math.ceil((pct / 100) * len(ordered)) - 1))
    return ordered[idx]

def fmt(value: float | None) -> str:
    return f"{value:.3f}" if value is not None else ""

grouped: dict[tuple[str, str], list[dict[str, str]]] = defaultdict(list)
for row in rows:
    grouped[(row["case"], row["endpoint"])].append(row)

summary: dict[tuple[str, str], dict[str, str]] = {}
for key, group in grouped.items():
    statuses = sorted({row.get("status", "") for row in group})
    totals = [value for row in group if (value := as_float(row.get("total_ms", ""))) is not None]
    ttfbs = [value for row in group if (value := as_float(row.get("ttfb_ms", ""))) is not None]
    status = ",".join(statuses)
    summary[key] = {
        "samples": str(len(group)),
        "status": status,
        "total_p50_ms": fmt(statistics.median(totals) if totals else None),
        "total_p95_ms": fmt(percentile(totals, 95)),
        "ttfb_p50_ms": fmt(statistics.median(ttfbs) if ttfbs else None),
        "ttfb_p95_ms": fmt(percentile(ttfbs, 95)),
    }

with summary_path.open("w", encoding="utf-8", newline="") as handle:
    writer = csv.writer(handle, delimiter="\t")
    writer.writerow(
        [
            "case",
            "endpoint",
            "status",
            "samples",
            "total_p50_ms",
            "total_p95_ms",
            "ttfb_p50_ms",
            "ttfb_p95_ms",
        ]
    )
    for case in ["direct", "local", "pa", "pd", "pu"]:
        for endpoint in ["models", "chat", "stream"]:
            data = summary.get((case, endpoint))
            if not data:
                continue
            writer.writerow(
                [
                    case,
                    endpoint,
                    data["status"],
                    data["samples"],
                    data["total_p50_ms"],
                    data["total_p95_ms"],
                    data["ttfb_p50_ms"],
                    data["ttfb_p95_ms"],
                ]
            )

with comparison_path.open("w", encoding="utf-8", newline="") as handle:
    writer = csv.writer(handle, delimiter="\t")
    writer.writerow(
        [
            "endpoint",
            "case",
            "status",
            "direct_total_p50_ms",
            "case_total_p50_ms",
            "delta_total_p50_ms",
            "direct_total_p95_ms",
            "case_total_p95_ms",
            "delta_total_p95_ms",
            "direct_ttfb_p50_ms",
            "case_ttfb_p50_ms",
            "delta_ttfb_p50_ms",
            "direct_ttfb_p95_ms",
            "case_ttfb_p95_ms",
            "delta_ttfb_p95_ms",
        ]
    )
    for endpoint in ["models", "chat", "stream"]:
        direct = summary.get(("direct", endpoint))
        if not direct:
            continue
        for case in ["local", "pa"]:
            row = summary.get((case, endpoint))
            if not row:
                continue
            direct_total_p50 = as_float(direct["total_p50_ms"])
            case_total_p50 = as_float(row["total_p50_ms"])
            direct_total_p95 = as_float(direct["total_p95_ms"])
            case_total_p95 = as_float(row["total_p95_ms"])
            direct_ttfb_p50 = as_float(direct["ttfb_p50_ms"])
            case_ttfb_p50 = as_float(row["ttfb_p50_ms"])
            direct_ttfb_p95 = as_float(direct["ttfb_p95_ms"])
            case_ttfb_p95 = as_float(row["ttfb_p95_ms"])
            writer.writerow(
                [
                    endpoint,
                    case,
                    row.get("status", ""),
                    fmt(direct_total_p50),
                    fmt(case_total_p50),
                    fmt(case_total_p50 - direct_total_p50)
                    if direct_total_p50 is not None and case_total_p50 is not None
                    else "",
                    fmt(direct_total_p95),
                    fmt(case_total_p95),
                    fmt(case_total_p95 - direct_total_p95)
                    if direct_total_p95 is not None and case_total_p95 is not None
                    else "",
                    fmt(direct_ttfb_p50),
                    fmt(case_ttfb_p50),
                    fmt(case_ttfb_p50 - direct_ttfb_p50)
                    if direct_ttfb_p50 is not None and case_ttfb_p50 is not None
                    else "",
                    fmt(direct_ttfb_p95),
                    fmt(case_ttfb_p95),
                    fmt(case_ttfb_p95 - direct_ttfb_p95)
                    if direct_ttfb_p95 is not None and case_ttfb_p95 is not None
                    else "",
                ]
            )
PY
}

assert_audit_contains() {
  local workspace="$1"
  local needle="$2"
  local log_path
  log_path="$(audit_report_for_workspace "$workspace")"
  [ -r "$log_path" ] || fail "audit log not readable for $workspace: $log_path"
  grep -q "\"event\":\"$needle\"" "$log_path" || fail "audit log $log_path missing event $needle"
}

assert_audit_lacks() {
  local workspace="$1"
  local needle="$2"
  local log_path
  log_path="$(audit_report_for_workspace "$workspace")"
  [ -r "$log_path" ] || fail "audit log not readable for $workspace: $log_path"
  if grep -q "\"event\":\"$needle\"" "$log_path"; then
    fail "audit log $log_path unexpectedly contains event $needle"
  fi
}

run_case() {
  local label="$1"
  local mode="$2"
  local expected_status="$3"
  local workspace="mmv-$label"
  local run_log="$OUT_DIR/$label.run.log"
  local stdout_path="$OUT_DIR/$label.guest.stdout"
  local guest_script
  mapfile -t runner_env < <(runner_env_args)
  local env_args=(
    "${runner_env[@]}"
    "MICROAGENT_MODEL_MEDIATION=$mode"
    "MICROAGENT_MODEL_POLICY_TIMEOUT=1s"
    "MICROAGENT_MODEL_POLICY_FILE="
  )
  if [ -n "$POLICY_URL" ]; then
    env_args+=("MICROAGENT_MODEL_POLICY_URL=$POLICY_URL")
  else
    env_args+=("MICROAGENT_MODEL_POLICY_URL=")
  fi

  reset_audit_log "$workspace"
  guest_script="$(cat <<'EOF'
set -eu

: "${MICROAGENT_MODEL_URL:?}"
: "${EXPECTED_STATUS:?}"
: "${REQUEST_MODEL:?}"
: "${CHAT_TOKENS:?}"
: "${STREAM_TOKENS:?}"
: "${SAMPLES:?}"

read_metrics() {
  prefix="$1"
  metrics="$2"
  set -- $metrics
  echo "${prefix}_STATUS=${1:-000}"
  echo "${prefix}_TTFB=${2:-0}"
  echo "${prefix}_TOTAL=${3:-0}"
  echo "${prefix}_BYTES=${4:-0}"
}

profile_curl() {
  out="$1"
  shift
  attempt=1
  while :; do
    metrics="$(curl -sS -o "$out" -w "%{http_code} %{time_starttransfer} %{time_total} %{size_download}" "$@" 2>/tmp/profile-curl.err || true)"
    status="${metrics%% *}"
    status="${status:-000}"
    if [ "$status" != "000" ] || [ "$attempt" -ge 50 ]; then
      if [ "$status" = "000" ] && [ -s /tmp/profile-curl.err ]; then
        cat /tmp/profile-curl.err >&2
      fi
      printf '%s\n' "$metrics"
      return
    fi
    attempt=$((attempt + 1))
    sleep 0.1
  done
}

chat_payload='{"model":"'"$REQUEST_MODEL"'","messages":[{"role":"user","content":"Reply with exactly PONG."}],"max_tokens":'"$CHAT_TOKENS"',"temperature":0,"stream":false}'
stream_payload='{"model":"'"$REQUEST_MODEL"'","messages":[{"role":"user","content":"Write one compact sentence about mediated host GPU workers."}],"max_tokens":'"$STREAM_TOKENS"',"temperature":0,"stream":true}'

sample=1
while [ "$sample" -le "$SAMPLES" ]; do
  metrics="$(profile_curl /tmp/model-body "$MICROAGENT_MODEL_URL/models")"
  set -- $metrics
  model_status="${1:-000}"
  model_ttfb="${2:-0}"
  model_total="${3:-0}"
  model_bytes="${4:-0}"
  read_metrics MODEL "$metrics"
  printf 'PROFILE\tmodels\t%s\t%s\t%s\t%s\t%s\t\n' "$sample" "$model_status" "$model_ttfb" "$model_total" "$model_bytes"
  test "$model_status" = "$EXPECTED_STATUS"
  if [ "$EXPECTED_STATUS" = "200" ]; then
    grep -q "$REQUEST_MODEL" /tmp/model-body
    echo "MODEL_REACHED=1"
  fi
  sample=$((sample + 1))
done

sample=1
while [ "$sample" -le "$SAMPLES" ]; do
  metrics="$(profile_curl /tmp/chat-body "$MICROAGENT_MODEL_URL/chat/completions" -H "Content-Type: application/json" -d "$chat_payload")"
  set -- $metrics
  chat_status="${1:-000}"
  chat_ttfb="${2:-0}"
  chat_total="${3:-0}"
  chat_bytes="${4:-0}"
  read_metrics CHAT "$metrics"
  printf 'PROFILE\tchat\t%s\t%s\t%s\t%s\t%s\t\n' "$sample" "$chat_status" "$chat_ttfb" "$chat_total" "$chat_bytes"
  test "$chat_status" = "$EXPECTED_STATUS"
  if [ "$EXPECTED_STATUS" = "200" ]; then
    grep -q '"choices"' /tmp/chat-body
    echo "CHAT_COMPATIBLE=1"
  fi
  sample=$((sample + 1))
done

sample=1
while [ "$sample" -le "$SAMPLES" ]; do
  metrics="$(profile_curl /tmp/stream-body -N "$MICROAGENT_MODEL_URL/chat/completions" -H "Content-Type: application/json" -d "$stream_payload")"
  set -- $metrics
  stream_status="${1:-000}"
  stream_ttfb="${2:-0}"
  stream_total="${3:-0}"
  stream_bytes="${4:-0}"
  stream_chunks="$(grep -c '^data:' /tmp/stream-body || true)"
  read_metrics STREAM "$metrics"
  echo "STREAM_CHUNKS=$stream_chunks"
  printf 'PROFILE\tstream\t%s\t%s\t%s\t%s\t%s\t%s\n' "$sample" "$stream_status" "$stream_ttfb" "$stream_total" "$stream_bytes" "$stream_chunks"
  test "$stream_status" = "$EXPECTED_STATUS"
  if [ "$EXPECTED_STATUS" = "200" ]; then
    grep -q '^data:' /tmp/stream-body
    echo "STREAM_COMPATIBLE=1"
  fi
  sample=$((sample + 1))
done
EOF
)"

  echo "microagent-e2e-model-mediation-vllm: case=$label mode=$mode expected_http=$expected_status"
  set_telemetry_phase "$label"
  if ! env "${env_args[@]}" "$CLI" run --name "$workspace" \
    --env "EXPECTED_STATUS=$expected_status" \
    --env "REQUEST_MODEL=$VLLM_SERVED_MODEL" \
    --env "CHAT_TOKENS=$CHAT_TOKENS" \
    --env "STREAM_TOKENS=$STREAM_TOKENS" \
    --env "SAMPLES=$SAMPLES" \
    "${RUN_FLAGS[@]}" sh -c "$guest_script" >"$run_log" 2>&1; then
    cat "$run_log" >&2
    fail "case $label failed"
  fi
  extract_guest_stdout "$run_log" "$stdout_path"
  capture_audit_log "$workspace"
  for prefix in MODEL CHAT STREAM; do
    grep -q "${prefix}_STATUS=$expected_status" "$stdout_path" || {
      cat "$stdout_path" >&2
      fail "case $label did not report expected $prefix status"
    }
  done
  if [ "$expected_status" = "200" ]; then
    grep -q "MODEL_REACHED=1" "$stdout_path" || {
      cat "$stdout_path" >&2
      fail "case $label did not reach vLLM model $VLLM_SERVED_MODEL"
    }
    grep -q "CHAT_COMPATIBLE=1" "$stdout_path" || fail "case $label did not get OpenAI chat response"
    grep -q "STREAM_COMPATIBLE=1" "$stdout_path" || fail "case $label did not get OpenAI stream response"
  fi
  if [ "$mode" != "off" ]; then
    assert_audit_contains "$workspace" "request_end"
  fi
  assert_index_clean
  assert_single_runner_reused
  summarize_audit "$label" "$workspace" "$expected_status"
  summarize_profiles "$label" "$expected_status" "$stdout_path"
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

MICROAGENT_E2E_MODEL_MEDIATION_RUNNER=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_LABEL="vllm" \
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_CASE_PREFIX="mmv" \
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_OUT_DIR="$OUT_DIR" \
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
