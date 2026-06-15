#!/usr/bin/env bash
#
# microagent-host-worker-probe.sh - experimental host worker execution probe.
#
# This is not a mediation or policy test. It answers the earlier question first:
# does a host-side OpenAI-compatible worker remain useful when a microVM reaches
# it over microagent's model bridge?
#
# The probe:
#   1. pulls/records the model in the selected state dir,
#   2. refuses to run if that model already has active runners,
#   3. starts one pinned host runner through `microagent model serve`,
#   4. measures direct host HTTP calls to /v1/models and /v1/chat/completions,
#   5. starts a model-paired workspace and measures the same calls in-guest,
#   6. cleans up the workspace and the runner it started.
#
# Required:
#   A host model runner resolvable by microagent. For the built-in runner this
#   means llama-server on PATH or MICROAGENT_LLAMA_SERVER set. Custom runners can
#   be supplied through MICROAGENT_MODEL_RUNNER_COMMAND and related env vars.
#
# Optional:
#   MICROAGENT_CLI                         microagent CLI (default: .build/dev/microagent)
#   MICROAGENT_FIRECRACKER                 Firecracker binary for Linux runs
#   MICROAGENT_HOST_WORKER_PROBE_MODEL_REF HuggingFace GGUF ref
#   MICROAGENT_HOST_WORKER_PROBE_IMAGE     guest image with curl
#   MICROAGENT_HOST_WORKER_PROBE_STATE_DIR state dir (default: ~/.microagent)
#   MICROAGENT_HOST_WORKER_PROBE_SAMPLES   measured samples per endpoint (default: 5)
#   MICROAGENT_HOST_WORKER_PROBE_WARMUPS   warmup calls per endpoint (default: 1)
#   MICROAGENT_HOST_WORKER_PROBE_WORKSPACE workspace name
#   MICROAGENT_HOST_WORKER_PROBE_REPORT    path to write final JSON report
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

CLI="${MICROAGENT_CLI:-$(e2e_exe "$ROOT/.build/dev/microagent")}"
MODEL_REF="${MICROAGENT_HOST_WORKER_PROBE_MODEL_REF:-Qwen/Qwen2.5-0.5B-Instruct-GGUF/qwen2.5-0.5b-instruct-q4_k_m.gguf}"
IMAGE="${MICROAGENT_HOST_WORKER_PROBE_IMAGE:-docker.io/curlimages/curl:latest}"
STATE_DIR="${MICROAGENT_HOST_WORKER_PROBE_STATE_DIR:-$HOME/.microagent}"
SAMPLES="${MICROAGENT_HOST_WORKER_PROBE_SAMPLES:-5}"
WARMUPS="${MICROAGENT_HOST_WORKER_PROBE_WARMUPS:-1}"
WS="${MICROAGENT_HOST_WORKER_PROBE_WORKSPACE:-host-worker-probe-$$}"
REPORT_PATH="${MICROAGENT_HOST_WORKER_PROBE_REPORT:-}"
STARTED_RUNNER=0
CREATE_FLAGS=()
START_FLAGS=()
CTRL_FLAGS=()

skip() { e2e_skip "microagent-host-worker-probe: $1"; }
fail() { echo "FAIL microagent-host-worker-probe: $1" >&2; exit 1; }

cleanup() {
  local status=$?
  set +e
  "$CLI" kill "$WS" --state-dir "$STATE_DIR" "${CTRL_FLAGS[@]}" >/dev/null 2>&1
  "$CLI" delete "$WS" --force --yes --state-dir "$STATE_DIR" "${CTRL_FLAGS[@]}" >/dev/null 2>&1
  if [ "$STARTED_RUNNER" -eq 1 ]; then
    "$CLI" model stop "$MODEL_REF" --state-dir "$STATE_DIR" >/dev/null 2>&1
  fi
  exit "$status"
}
trap cleanup EXIT

json_get() {
  local field="$1"
  python3 -c '
import json
import sys

field = sys.argv[1]
doc = json.load(sys.stdin)
value = doc
for part in field.split("."):
    value = value[part]
if isinstance(value, (dict, list)):
    print(json.dumps(value))
else:
    print(value)
' "$field"
}

runner_count_for_model() {
  local model_ref="$1"
  "$CLI" --json model runners --state-dir "$STATE_DIR" 2>/dev/null | python3 -c '
import json
import sys

model_ref = sys.argv[1]
try:
    doc = json.load(sys.stdin) or {}
except Exception:
    print(0)
    raise SystemExit(0)
runners = doc.get("runners") or []
print(sum(1 for runner in runners if runner.get("model_ref") == model_ref))
' "$model_ref"
}

host_benchmark() {
  local base_url="$1"
  python3 - "$base_url" "$SAMPLES" "$WARMUPS" <<'PY'
import json
import statistics
import sys
import time
import urllib.error
import urllib.request

base_url = sys.argv[1].rstrip("/")
samples = int(sys.argv[2])
warmups = int(sys.argv[3])
timeout = 180
chat_payload = json.dumps({
    "messages": [{"role": "user", "content": "Reply with exactly: PONG"}],
    "max_tokens": 16,
    "temperature": 0,
}).encode("utf-8")

if samples <= 0:
    raise SystemExit("samples must be > 0")
if warmups < 0:
    raise SystemExit("warmups must be >= 0")

def request_models():
    req = urllib.request.Request(base_url + "/models", method="GET")
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        body = resp.read(4 * 1024 * 1024)
    doc = json.loads(body)
    if "object" not in doc and "data" not in doc:
        raise RuntimeError("/models response did not look OpenAI-compatible")

def request_chat():
    req = urllib.request.Request(
        base_url + "/chat/completions",
        data=chat_payload,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        body = resp.read(4 * 1024 * 1024)
    doc = json.loads(body)
    if not doc.get("choices"):
        raise RuntimeError("/chat/completions response did not contain choices")

def run(label, fn):
    values = []
    total = warmups + samples
    for i in range(total):
        start = time.perf_counter()
        fn()
        elapsed_ms = (time.perf_counter() - start) * 1000
        if i >= warmups:
            values.append(round(elapsed_ms, 3))
    ordered = sorted(values)
    p95_index = min(len(ordered) - 1, max(0, int(len(ordered) * 0.95 + 0.999999) - 1))
    return {
        "sample_count": len(values),
        "samples_ms": values,
        "min_ms": ordered[0],
        "median_ms": round(statistics.median(ordered), 3),
        "mean_ms": round(statistics.fmean(ordered), 3),
        "p95_ms": ordered[p95_index],
        "max_ms": ordered[-1],
    }

try:
    report = {
        "models": run("models", request_models),
        "chat": run("chat", request_chat),
    }
except (urllib.error.URLError, TimeoutError, RuntimeError, json.JSONDecodeError) as err:
    raise SystemExit(f"host benchmark failed: {err}") from err

print(json.dumps(report, sort_keys=True))
PY
}

combine_report() {
  local host_json="$1"
  local guest_json="$2"
  local backend="$3"
  local canonical="$4"
  local runner_json="$5"
  python3 - "$host_json" "$guest_json" "$backend" "$canonical" "$runner_json" <<'PY'
import json
import statistics
import sys

host_raw = json.loads(sys.argv[1])
guest_raw = json.loads(sys.argv[2])
backend = sys.argv[3]
canonical = sys.argv[4]
runner = json.loads(sys.argv[5])

def summarize(values_ms):
    values = [round(float(value), 3) for value in values_ms]
    ordered = sorted(values)
    p95_index = min(len(ordered) - 1, max(0, int(len(ordered) * 0.95 + 0.999999) - 1))
    return {
        "sample_count": len(values),
        "samples_ms": values,
        "min_ms": ordered[0],
        "median_ms": round(statistics.median(ordered), 3),
        "mean_ms": round(statistics.fmean(ordered), 3),
        "p95_ms": ordered[p95_index],
        "max_ms": ordered[-1],
    }

def normalize(report):
    normalized = {}
    for endpoint in ("models", "chat"):
        data = report[endpoint]
        if "samples_seconds" in data:
            values = [float(value) * 1000 for value in data["samples_seconds"]]
        else:
            values = data["samples_ms"]
        normalized[endpoint] = summarize(values)
    return normalized

host = normalize(host_raw)
guest = normalize(guest_raw)

def compare(endpoint):
    host_median = host[endpoint]["median_ms"]
    guest_median = guest[endpoint]["median_ms"]
    ratio = None
    if host_median > 0:
        ratio = round(guest_median / host_median, 3)
    return {
        "host_median_ms": host_median,
        "guest_median_ms": guest_median,
        "delta_ms": round(guest_median - host_median, 3),
        "guest_to_host_ratio": ratio,
    }

public_runner = {
    "model_ref": runner.get("model_ref"),
    "engine": runner.get("engine"),
    "host": runner.get("host"),
    "port": runner.get("port"),
    "pid": runner.get("pid"),
    "runner_config_digest": runner.get("runner_config_digest"),
}
report = {
    "backend": backend,
    "model_ref": canonical,
    "runner": public_runner,
    "host": host,
    "guest": guest,
    "overhead": {
        "models": compare("models"),
        "chat": compare("chat"),
    },
}
print(json.dumps(report, indent=2, sort_keys=True))
PY
}

default_backend() {
  case "$(uname -s):$(uname -m)" in
    Linux:x86_64|Linux:amd64)
      printf '%s\n' firecracker
      ;;
    *)
      printf '%s\n' unsupported
      ;;
  esac
}

if [ ! -x "$CLI" ]; then
  skip "CLI not found at $CLI (run scripts/dev/build-local.sh)"
fi
case "$SAMPLES" in
  ''|*[!0-9]*) fail "MICROAGENT_HOST_WORKER_PROBE_SAMPLES must be a positive integer" ;;
esac
case "$WARMUPS" in
  ''|*[!0-9]*) fail "MICROAGENT_HOST_WORKER_PROBE_WARMUPS must be a non-negative integer" ;;
esac
if [ "$SAMPLES" -le 0 ]; then
  fail "MICROAGENT_HOST_WORKER_PROBE_SAMPLES must be > 0"
fi

BACKEND="${MICROAGENT_E2E_BACKEND:-$(default_backend)}"
case "$BACKEND" in
  firecracker)
    if [ -z "${MICROAGENT_FIRECRACKER:-}" ] || [ ! -x "${MICROAGENT_FIRECRACKER:-/nonexistent}" ]; then
      skip "MICROAGENT_FIRECRACKER not set/executable"
    fi
    if [ ! -e /dev/kvm ]; then
      skip "/dev/kvm not available"
    fi
    CREATE_FLAGS=(--backend firecracker)
    START_FLAGS=(--backend firecracker)
    CTRL_FLAGS=(--backend firecracker)
    ;;
  *)
    skip "unsupported host/backend for host worker probe: os=$(uname -s) arch=$(uname -m) backend=$BACKEND"
    ;;
esac

echo "microagent-host-worker-probe: backend=$BACKEND model=$MODEL_REF image=$IMAGE samples=$SAMPLES warmups=$WARMUPS"
echo "microagent-host-worker-probe: pulling or refreshing model record"
PULL_JSON="$("$CLI" --json model pull "$MODEL_REF" --state-dir "$STATE_DIR")" || fail "model pull failed"
CANONICAL="$(printf '%s' "$PULL_JSON" | json_get model_ref)"

existing_count="$(runner_count_for_model "$CANONICAL")"
if [ "$existing_count" -ne 0 ]; then
  skip "model $CANONICAL already has $existing_count active runner(s); stop them before running this destructive probe"
fi

echo "microagent-host-worker-probe: starting pinned host runner"
RUNNER_JSON="$("$CLI" --json model serve "$MODEL_REF" --state-dir "$STATE_DIR")" || fail "model serve failed"
STARTED_RUNNER=1
RUNNER_HOST="$(printf '%s' "$RUNNER_JSON" | json_get host)"
RUNNER_PORT="$(printf '%s' "$RUNNER_JSON" | json_get port)"
RUNNER_BASE_URL="http://$RUNNER_HOST:$RUNNER_PORT/v1"

echo "microagent-host-worker-probe: measuring direct host calls at $RUNNER_BASE_URL"
HOST_JSON="$(host_benchmark "$RUNNER_BASE_URL")" || fail "direct host benchmark failed"

"$CLI" delete "$WS" --force --yes --state-dir "$STATE_DIR" "${CTRL_FLAGS[@]}" >/dev/null 2>&1 || true
echo "microagent-host-worker-probe: creating and starting paired workspace $WS"
"$CLI" create "$WS" --image "$IMAGE" --model "$MODEL_REF" --state-dir "$STATE_DIR" "${CREATE_FLAGS[@]}" >/dev/null || fail "create --model failed"
"$CLI" start "$WS" --state-dir "$STATE_DIR" "${START_FLAGS[@]}" >/dev/null || fail "start failed"
if ! e2e_wait_exec_ready "$CLI" "$STATE_DIR" "$WS" 90; then
  "$CLI" --json status "$WS" --state-dir "$STATE_DIR" "${CTRL_FLAGS[@]}" >&2 || true
  fail "workspace exec service did not become ready"
fi

read -r -d '' GUEST_BENCH <<'SH' || true
set -eu

measure_models() {
  i=0
  values=""
  total=$((PROBE_WARMUPS + PROBE_SAMPLES))
  while [ "$i" -lt "$total" ]; do
    body="$(mktemp)"
    elapsed="$(curl -sS -o "$body" -w '%{time_total}' "$MICROAGENT_MODEL_URL/models")"
    if ! grep -Eq '"object"|"data"' "$body"; then
      echo "models response did not look OpenAI-compatible" >&2
      cat "$body" >&2
      rm -f "$body"
      exit 1
    fi
    rm -f "$body"
    if [ "$i" -ge "$PROBE_WARMUPS" ]; then
      values="${values}${values:+,}${elapsed}"
    fi
    i=$((i + 1))
  done
  printf '%s\n' "$values"
}

measure_chat() {
  payload='{"messages":[{"role":"user","content":"Reply with exactly: PONG"}],"max_tokens":16,"temperature":0}'
  i=0
  values=""
  total=$((PROBE_WARMUPS + PROBE_SAMPLES))
  while [ "$i" -lt "$total" ]; do
    body="$(mktemp)"
    elapsed="$(curl -sS -o "$body" -w '%{time_total}' "$MICROAGENT_MODEL_URL/chat/completions" -H "Content-Type: application/json" -d "$payload")"
    if ! grep -q '"choices"' "$body"; then
      echo "chat response did not contain choices" >&2
      cat "$body" >&2
      rm -f "$body"
      exit 1
    fi
    rm -f "$body"
    if [ "$i" -ge "$PROBE_WARMUPS" ]; then
      values="${values}${values:+,}${elapsed}"
    fi
    i=$((i + 1))
  done
  printf '%s\n' "$values"
}

models_values="$(measure_models)"
chat_values="$(measure_chat)"
printf '{"models":{"samples_seconds":[%s]},"chat":{"samples_seconds":[%s]}}\n' "$models_values" "$chat_values"
SH

echo "microagent-host-worker-probe: measuring in-guest calls over the model bridge"
GUEST_JSON="$("$CLI" exec "$WS" --state-dir "$STATE_DIR" -env "PROBE_SAMPLES=$SAMPLES" -env "PROBE_WARMUPS=$WARMUPS" -- sh -c "$GUEST_BENCH")" || fail "guest benchmark failed"

REPORT_JSON="$(combine_report "$HOST_JSON" "$GUEST_JSON" "$BACKEND" "$CANONICAL" "$RUNNER_JSON")"
if [ -n "$REPORT_PATH" ]; then
  mkdir -p "$(dirname "$REPORT_PATH")"
  printf '%s\n' "$REPORT_JSON" >"$REPORT_PATH"
  echo "microagent-host-worker-probe: wrote report to $REPORT_PATH"
fi

python3 - "$REPORT_JSON" <<'PY'
import json
import sys

report = json.loads(sys.argv[1])
for endpoint in ("models", "chat"):
    item = report["overhead"][endpoint]
    print(
        "microagent-host-worker-probe: "
        f"{endpoint} median host={item['host_median_ms']:.3f}ms "
        f"guest={item['guest_median_ms']:.3f}ms "
        f"delta={item['delta_ms']:.3f}ms "
        f"ratio={item['guest_to_host_ratio']}"
    )
PY

echo "microagent-host-worker-probe: report"
printf '%s\n' "$REPORT_JSON"
echo "PASS microagent-host-worker-probe: host worker direct and in-guest execution paths succeeded"
