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
#   MICROAGENT_HOST_WORKER_PROBE_CONCURRENCY
#                                           comma-separated worker counts (default: 1)
#   MICROAGENT_HOST_WORKER_PROBE_CHAT_PROFILE
#                                           tiny or sustained (default: tiny)
#   MICROAGENT_HOST_WORKER_PROBE_CHAT_TOKENS
#                                           max tokens for non-streaming chat (default: 16)
#   MICROAGENT_HOST_WORKER_PROBE_STREAM     measure streaming chat too: 0/1 (default: 0)
#   MICROAGENT_HOST_WORKER_PROBE_STREAM_TOKENS
#                                           max tokens for streaming chat (default: 128)
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
CONCURRENCY="${MICROAGENT_HOST_WORKER_PROBE_CONCURRENCY:-1}"
CHAT_PROFILE="${MICROAGENT_HOST_WORKER_PROBE_CHAT_PROFILE:-tiny}"
CHAT_TOKENS="${MICROAGENT_HOST_WORKER_PROBE_CHAT_TOKENS:-16}"
STREAM="${MICROAGENT_HOST_WORKER_PROBE_STREAM:-0}"
STREAM_TOKENS="${MICROAGENT_HOST_WORKER_PROBE_STREAM_TOKENS:-128}"
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
  python3 - "$base_url" "$SAMPLES" "$WARMUPS" "$CONCURRENCY" "$CHAT_PROFILE" "$CHAT_TOKENS" "$STREAM" "$STREAM_TOKENS" <<'PY'
import concurrent.futures
import json
import statistics
import sys
import threading
import time
import urllib.error
import urllib.request

base_url = sys.argv[1].rstrip("/")
samples = int(sys.argv[2])
warmups = int(sys.argv[3])
levels = [int(part) for part in sys.argv[4].replace(",", " ").split()]
chat_profile = sys.argv[5]
chat_tokens = int(sys.argv[6])
stream_enabled = sys.argv[7] == "1"
stream_tokens = int(sys.argv[8])
timeout = 180

if samples <= 0:
    raise SystemExit("samples must be > 0")
if warmups < 0:
    raise SystemExit("warmups must be >= 0")
if not levels or any(level <= 0 for level in levels):
    raise SystemExit("concurrency levels must be positive integers")
if chat_profile not in ("tiny", "sustained"):
    raise SystemExit("chat profile must be tiny or sustained")
if chat_tokens <= 0:
    raise SystemExit("chat tokens must be > 0")
if stream_tokens <= 0:
    raise SystemExit("stream tokens must be > 0")
total = warmups + samples

def request_models():
    start = time.perf_counter()
    req = urllib.request.Request(base_url + "/models", method="GET")
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        body = resp.read(4 * 1024 * 1024)
    elapsed_ms = (time.perf_counter() - start) * 1000
    doc = json.loads(body)
    if "object" not in doc and "data" not in doc:
        raise RuntimeError("/models response did not look OpenAI-compatible")
    return {"elapsed_ms": round(elapsed_ms, 3)}

def request_chat():
    if chat_profile == "sustained":
        content = "Write one compact paragraph about mediated host GPU workers."
    else:
        content = "Reply with exactly: PONG"
    payload = json.dumps({
        "messages": [{"role": "user", "content": content}],
        "max_tokens": chat_tokens,
        "temperature": 0,
    }).encode("utf-8")
    start = time.perf_counter()
    req = urllib.request.Request(
        base_url + "/chat/completions",
        data=payload,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        body = resp.read(4 * 1024 * 1024)
    elapsed_ms = (time.perf_counter() - start) * 1000
    doc = json.loads(body)
    if not doc.get("choices"):
        raise RuntimeError("/chat/completions response did not contain choices")
    return {"elapsed_ms": round(elapsed_ms, 3)}

def request_stream():
    payload = json.dumps({
        "messages": [
            {
                "role": "user",
                "content": "Write one compact paragraph about mediated host GPU workers.",
            }
        ],
        "max_tokens": stream_tokens,
        "temperature": 0,
        "stream": True,
    }).encode("utf-8")
    req = urllib.request.Request(
        base_url + "/chat/completions",
        data=payload,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    start = time.perf_counter()
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        first = resp.read(1)
        if not first:
            raise RuntimeError("stream response was empty")
        ttfb_ms = (time.perf_counter() - start) * 1000
        body = first + resp.read(64 * 1024 * 1024)
    elapsed_ms = (time.perf_counter() - start) * 1000
    if b"[DONE]" not in body and b'"choices"' not in body:
        raise RuntimeError("stream response did not look OpenAI-compatible")
    return {
        "elapsed_ms": round(elapsed_ms, 3),
        "ttfb_ms": round(ttfb_ms, 3),
        "bytes": len(body),
        "chunks": sum(1 for line in body.splitlines() if line.startswith(b"data:")),
    }

def summarize_number(values, name, unit):
    ordered = sorted(values)
    p95_index = min(len(ordered) - 1, max(0, int(len(ordered) * 0.95 + 0.999999) - 1))
    unit_suffix = f"_{unit}" if unit else ""
    return {
        f"{name}_samples{unit_suffix}": values,
        f"{name}_min{unit_suffix}": ordered[0],
        f"{name}_median{unit_suffix}": round(statistics.median(ordered), 3),
        f"{name}_mean{unit_suffix}": round(statistics.fmean(ordered), 3),
        f"{name}_p95{unit_suffix}": ordered[p95_index],
        f"{name}_max{unit_suffix}": ordered[-1],
    }

def summarize(samples_for_endpoint, concurrency):
    elapsed_values = [item["elapsed_ms"] for item in samples_for_endpoint]
    ordered = sorted(elapsed_values)
    p95_index = min(len(ordered) - 1, max(0, int(len(ordered) * 0.95 + 0.999999) - 1))
    summary = {
        "concurrency": concurrency,
        "samples_per_worker": samples,
        "warmups_per_worker": warmups,
        "sample_count": len(elapsed_values),
        "samples_ms": elapsed_values,
        "min_ms": ordered[0],
        "median_ms": round(statistics.median(ordered), 3),
        "mean_ms": round(statistics.fmean(ordered), 3),
        "p95_ms": ordered[p95_index],
        "max_ms": ordered[-1],
    }
    optional_metrics = (
        ("ttfb_ms", "ttfb", "ms"),
        ("bytes", "bytes", ""),
        ("chunks", "chunks", ""),
    )
    for sample_key, summary_prefix, unit in optional_metrics:
        metric_values = [item[sample_key] for item in samples_for_endpoint if sample_key in item]
        if metric_values:
            summary.update(summarize_number(metric_values, summary_prefix, unit))
    return summary

def worker(fn, barrier):
    values = []
    barrier.wait()
    for i in range(total):
        sample = fn()
        if i >= warmups:
            values.append(sample)
    return values

def run_level(fn, concurrency):
    values = []
    barrier = threading.Barrier(concurrency)
    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
        futures = [pool.submit(worker, fn, barrier) for _ in range(concurrency)]
        for future in concurrent.futures.as_completed(futures):
            values.extend(future.result())
    return summarize(values, concurrency)

try:
    report = {"levels": {}}
    endpoints = [
        ("models", request_models),
        ("chat", request_chat),
    ]
    if stream_enabled:
        endpoints.append(("stream", request_stream))
    for level in levels:
        report["levels"][str(level)] = {}
        for endpoint, fn in endpoints:
            report["levels"][str(level)][endpoint] = run_level(fn, level)
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
  python3 - "$host_json" "$guest_json" "$backend" "$canonical" "$runner_json" "$CHAT_PROFILE" "$CHAT_TOKENS" "$STREAM" "$STREAM_TOKENS" <<'PY'
import json
import statistics
import sys

host_raw = json.loads(sys.argv[1])
guest_raw = json.loads(sys.argv[2])
backend = sys.argv[3]
canonical = sys.argv[4]
runner = json.loads(sys.argv[5])
chat_profile = sys.argv[6]
chat_tokens = int(sys.argv[7])
stream_enabled = sys.argv[8] == "1"
stream_tokens = int(sys.argv[9])

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

def summarize_number(values_raw, name, unit):
    values = [round(float(value), 3) for value in values_raw]
    ordered = sorted(values)
    p95_index = min(len(ordered) - 1, max(0, int(len(ordered) * 0.95 + 0.999999) - 1))
    unit_suffix = f"_{unit}" if unit else ""
    return {
        f"{name}_samples{unit_suffix}": values,
        f"{name}_min{unit_suffix}": ordered[0],
        f"{name}_median{unit_suffix}": round(statistics.median(ordered), 3),
        f"{name}_mean{unit_suffix}": round(statistics.fmean(ordered), 3),
        f"{name}_p95{unit_suffix}": ordered[p95_index],
        f"{name}_max{unit_suffix}": ordered[-1],
    }

def endpoint_order(keys):
    preferred = ["models", "chat", "stream"]
    present = set(keys)
    return [key for key in preferred if key in present] + sorted(present - set(preferred))

def endpoint_summary(data):
    if "samples_seconds" in data:
        values = [float(value) * 1000 for value in data["samples_seconds"]]
    else:
        values = data["samples_ms"]
    summary = summarize(values)
    if "ttfb_samples_seconds" in data:
        summary.update(summarize_number([float(value) * 1000 for value in data["ttfb_samples_seconds"]], "ttfb", "ms"))
    elif "ttfb_samples_ms" in data:
        summary.update(summarize_number(data["ttfb_samples_ms"], "ttfb", "ms"))
    for key, name in (("bytes_samples", "bytes"), ("chunks_samples", "chunks")):
        if key in data:
            summary.update(summarize_number(data[key], name, ""))
    return summary

def normalize(report):
    if "levels" not in report:
        report = {"levels": {"1": report}}
    normalized = {"levels": {}}
    for level, level_report in report["levels"].items():
        normalized["levels"][str(level)] = {}
        for endpoint in endpoint_order(level_report.keys()):
            data = level_report[endpoint]
            summary = endpoint_summary(data)
            summary["concurrency"] = int(level)
            for key in ("samples_per_worker", "warmups_per_worker"):
                if key in data:
                    summary[key] = data[key]
            normalized["levels"][str(level)][endpoint] = summary
    return normalized

host = normalize(host_raw)
guest = normalize(guest_raw)

def compare(level, endpoint):
    host_summary = host["levels"][level][endpoint]
    guest_summary = guest["levels"][level][endpoint]
    host_median = host_summary["median_ms"]
    guest_median = guest_summary["median_ms"]
    ratio = None
    if host_median > 0:
        ratio = round(guest_median / host_median, 3)
    result = {
        "host_max_ms": host_summary["max_ms"],
        "host_median_ms": host_median,
        "host_p95_ms": host_summary["p95_ms"],
        "guest_max_ms": guest_summary["max_ms"],
        "guest_median_ms": guest_median,
        "guest_p95_ms": guest_summary["p95_ms"],
        "delta_ms": round(guest_median - host_median, 3),
        "p95_delta_ms": round(guest_summary["p95_ms"] - host_summary["p95_ms"], 3),
        "max_delta_ms": round(guest_summary["max_ms"] - host_summary["max_ms"], 3),
        "guest_to_host_ratio": ratio,
    }
    if "ttfb_median_ms" in host_summary and "ttfb_median_ms" in guest_summary:
        result.update({
            "host_ttfb_median_ms": host_summary["ttfb_median_ms"],
            "host_ttfb_p95_ms": host_summary["ttfb_p95_ms"],
            "guest_ttfb_median_ms": guest_summary["ttfb_median_ms"],
            "guest_ttfb_p95_ms": guest_summary["ttfb_p95_ms"],
            "ttfb_delta_ms": round(guest_summary["ttfb_median_ms"] - host_summary["ttfb_median_ms"], 3),
            "ttfb_p95_delta_ms": round(guest_summary["ttfb_p95_ms"] - host_summary["ttfb_p95_ms"], 3),
        })
    return result

levels = sorted(host["levels"].keys(), key=lambda value: int(value))
endpoints = endpoint_order(host["levels"][levels[0]].keys())
matrix = {}
for level in levels:
    matrix[level] = {
        "host": host["levels"][level],
        "guest": guest["levels"][level],
        "overhead": {},
    }
    for endpoint in endpoints:
        if endpoint in host["levels"][level] and endpoint in guest["levels"][level]:
            matrix[level]["overhead"][endpoint] = compare(level, endpoint)

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
    "concurrency_levels": [int(level) for level in levels],
    "endpoints": endpoints,
    "matrix": matrix,
    "model_ref": canonical,
    "request_profiles": {
        "chat": {
            "profile": chat_profile,
            "max_tokens": chat_tokens,
            "stream": False,
        },
        "stream": {
            "enabled": stream_enabled,
            "max_tokens": stream_tokens,
        },
    },
    "runner": public_runner,
    "samples_per_worker": matrix[levels[0]]["host"]["models"].get("samples_per_worker", matrix[levels[0]]["host"]["models"].get("sample_count", 0) // int(levels[0])),
    "warmups_per_worker": matrix[levels[0]]["host"]["models"].get("warmups_per_worker"),
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
case "$CHAT_TOKENS" in
  ''|*[!0-9]*) fail "MICROAGENT_HOST_WORKER_PROBE_CHAT_TOKENS must be a positive integer" ;;
esac
case "$STREAM_TOKENS" in
  ''|*[!0-9]*) fail "MICROAGENT_HOST_WORKER_PROBE_STREAM_TOKENS must be a positive integer" ;;
esac
if [ "$SAMPLES" -le 0 ]; then
  fail "MICROAGENT_HOST_WORKER_PROBE_SAMPLES must be > 0"
fi
if [ "$CHAT_TOKENS" -le 0 ]; then
  fail "MICROAGENT_HOST_WORKER_PROBE_CHAT_TOKENS must be > 0"
fi
if [ "$STREAM_TOKENS" -le 0 ]; then
  fail "MICROAGENT_HOST_WORKER_PROBE_STREAM_TOKENS must be > 0"
fi
case "$CHAT_PROFILE" in
  tiny|sustained)
    ;;
  *)
    fail "MICROAGENT_HOST_WORKER_PROBE_CHAT_PROFILE must be tiny or sustained"
    ;;
esac
case "$STREAM" in
  1|true|TRUE|yes|YES)
    STREAM=1
    ;;
  0|false|FALSE|no|NO)
    STREAM=0
    ;;
  *)
    fail "MICROAGENT_HOST_WORKER_PROBE_STREAM must be 0/1, true/false, or yes/no"
    ;;
esac
CONCURRENCY_SPACES="$(printf '%s' "$CONCURRENCY" | tr ',' ' ')"
if [ -z "$(printf '%s' "$CONCURRENCY_SPACES" | tr -d '[:space:]')" ]; then
  fail "MICROAGENT_HOST_WORKER_PROBE_CONCURRENCY must include at least one positive integer"
fi
for level in $CONCURRENCY_SPACES; do
  case "$level" in
    ''|*[!0-9]*) fail "MICROAGENT_HOST_WORKER_PROBE_CONCURRENCY must be comma-separated positive integers" ;;
  esac
  if [ "$level" -le 0 ]; then
    fail "MICROAGENT_HOST_WORKER_PROBE_CONCURRENCY levels must be > 0"
  fi
done

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

echo "microagent-host-worker-probe: backend=$BACKEND model=$MODEL_REF image=$IMAGE samples=$SAMPLES warmups=$WARMUPS concurrency=$CONCURRENCY chat_profile=$CHAT_PROFILE chat_tokens=$CHAT_TOKENS stream=$STREAM stream_tokens=$STREAM_TOKENS"
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

join_values() {
  result=""
  while IFS= read -r value; do
    [ -n "$value" ] || continue
    result="${result}${result:+,}${value}"
  done <"$1"
  printf '%s' "$result"
}

measure_models_worker() {
  out="$1"
  i=0
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
      printf '%s\n' "$elapsed" >>"$out"
    fi
    i=$((i + 1))
  done
}

measure_chat_worker() {
  out="$1"
  if [ "$PROBE_CHAT_PROFILE" = "sustained" ]; then
    payload='{"messages":[{"role":"user","content":"Write one compact paragraph about mediated host GPU workers."}],"max_tokens":'"$PROBE_CHAT_TOKENS"',"temperature":0}'
  else
    payload='{"messages":[{"role":"user","content":"Reply with exactly: PONG"}],"max_tokens":'"$PROBE_CHAT_TOKENS"',"temperature":0}'
  fi
  i=0
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
      printf '%s\n' "$elapsed" >>"$out"
    fi
    i=$((i + 1))
  done
}

measure_stream_worker() {
  out="$1"
  ttfb_out="$2"
  bytes_out="$3"
  chunks_out="$4"
  payload='{"messages":[{"role":"user","content":"Write one compact paragraph about mediated host GPU workers."}],"max_tokens":'"$PROBE_STREAM_TOKENS"',"temperature":0,"stream":true}'
  i=0
  total=$((PROBE_WARMUPS + PROBE_SAMPLES))
  while [ "$i" -lt "$total" ]; do
    body="$(mktemp)"
    metrics="$(curl -sS -N -o "$body" -w '%{time_starttransfer} %{time_total} %{size_download}' "$MICROAGENT_MODEL_URL/chat/completions" -H "Content-Type: application/json" -d "$payload")"
    set -- $metrics
    ttfb="$1"
    elapsed="$2"
    bytes="$3"
    if ! grep -Eq '\[DONE\]|"choices"' "$body"; then
      echo "stream response did not look OpenAI-compatible" >&2
      cat "$body" >&2
      rm -f "$body"
      exit 1
    fi
    chunks="$(grep -c '^data:' "$body" 2>/dev/null || true)"
    chunks="${chunks:-0}"
    rm -f "$body"
    if [ "$i" -ge "$PROBE_WARMUPS" ]; then
      printf '%s\n' "$elapsed" >>"$out"
      printf '%s\n' "$ttfb" >>"$ttfb_out"
      printf '%s\n' "$bytes" >>"$bytes_out"
      printf '%s\n' "$chunks" >>"$chunks_out"
    fi
    i=$((i + 1))
  done
}

measure_models() {
  level="$1"
  tmp="$(mktemp -d)"
  pids=""
  worker=1
  while [ "$worker" -le "$level" ]; do
    measure_models_worker "$tmp/$worker" &
    pids="$pids $!"
    worker=$((worker + 1))
  done
  status=0
  for pid in $pids; do
    wait "$pid" || status=1
  done
  if [ "$status" -ne 0 ]; then
    rm -rf "$tmp"
    exit "$status"
  fi
  values_file="$tmp/values"
  : >"$values_file"
  worker=1
  while [ "$worker" -le "$level" ]; do
    cat "$tmp/$worker" >>"$values_file"
    worker=$((worker + 1))
  done
  join_values "$values_file"
  rm -rf "$tmp"
}

measure_chat() {
  level="$1"
  tmp="$(mktemp -d)"
  pids=""
  worker=1
  while [ "$worker" -le "$level" ]; do
    measure_chat_worker "$tmp/$worker" &
    pids="$pids $!"
    worker=$((worker + 1))
  done
  status=0
  for pid in $pids; do
    wait "$pid" || status=1
  done
  if [ "$status" -ne 0 ]; then
    rm -rf "$tmp"
    exit "$status"
  fi
  values_file="$tmp/values"
  : >"$values_file"
  worker=1
  while [ "$worker" -le "$level" ]; do
    cat "$tmp/$worker" >>"$values_file"
    worker=$((worker + 1))
  done
  join_values "$values_file"
  rm -rf "$tmp"
}

measure_stream() {
  level="$1"
  tmp="$(mktemp -d)"
  pids=""
  worker=1
  while [ "$worker" -le "$level" ]; do
    measure_stream_worker "$tmp/$worker.total" "$tmp/$worker.ttfb" "$tmp/$worker.bytes" "$tmp/$worker.chunks" &
    pids="$pids $!"
    worker=$((worker + 1))
  done
  status=0
  for pid in $pids; do
    wait "$pid" || status=1
  done
  if [ "$status" -ne 0 ]; then
    rm -rf "$tmp"
    exit "$status"
  fi
  total_file="$tmp/total"
  ttfb_file="$tmp/ttfb"
  bytes_file="$tmp/bytes"
  chunks_file="$tmp/chunks"
  : >"$total_file"
  : >"$ttfb_file"
  : >"$bytes_file"
  : >"$chunks_file"
  worker=1
  while [ "$worker" -le "$level" ]; do
    cat "$tmp/$worker.total" >>"$total_file"
    cat "$tmp/$worker.ttfb" >>"$ttfb_file"
    cat "$tmp/$worker.bytes" >>"$bytes_file"
    cat "$tmp/$worker.chunks" >>"$chunks_file"
    worker=$((worker + 1))
  done
  printf '"samples_seconds":[%s],"ttfb_samples_seconds":[%s],"bytes_samples":[%s],"chunks_samples":[%s]' "$(join_values "$total_file")" "$(join_values "$ttfb_file")" "$(join_values "$bytes_file")" "$(join_values "$chunks_file")"
  rm -rf "$tmp"
}

printf '{"levels":{'
first_level=1
for level in $(printf '%s' "$PROBE_CONCURRENCY" | tr ',' ' '); do
  models_values="$(measure_models "$level")"
  chat_values="$(measure_chat "$level")"
  if [ "$first_level" -eq 0 ]; then
    printf ','
  fi
  first_level=0
  printf '"%s":{"models":{"samples_seconds":[%s],"samples_per_worker":%s,"warmups_per_worker":%s},"chat":{"samples_seconds":[%s],"samples_per_worker":%s,"warmups_per_worker":%s}' "$level" "$models_values" "$PROBE_SAMPLES" "$PROBE_WARMUPS" "$chat_values" "$PROBE_SAMPLES" "$PROBE_WARMUPS"
  if [ "$PROBE_STREAM" = "1" ]; then
    stream_values="$(measure_stream "$level")"
    printf ',"stream":{%s,"samples_per_worker":%s,"warmups_per_worker":%s}' "$stream_values" "$PROBE_SAMPLES" "$PROBE_WARMUPS"
  fi
  printf '}'
done
printf '}}\n'
SH

echo "microagent-host-worker-probe: measuring in-guest calls over the model bridge"
GUEST_JSON="$("$CLI" exec "$WS" --state-dir "$STATE_DIR" -env "PROBE_SAMPLES=$SAMPLES" -env "PROBE_WARMUPS=$WARMUPS" -env "PROBE_CONCURRENCY=$CONCURRENCY" -env "PROBE_CHAT_PROFILE=$CHAT_PROFILE" -env "PROBE_CHAT_TOKENS=$CHAT_TOKENS" -env "PROBE_STREAM=$STREAM" -env "PROBE_STREAM_TOKENS=$STREAM_TOKENS" -- sh -c "$GUEST_BENCH")" || fail "guest benchmark failed"

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
for level in report["concurrency_levels"]:
    level_report = report["matrix"][str(level)]
    for endpoint in report["endpoints"]:
        if endpoint not in level_report["overhead"]:
            continue
        item = level_report["overhead"][endpoint]
        line = (
            "microagent-host-worker-probe: "
            f"c={level} {endpoint} median host={item['host_median_ms']:.3f}ms "
            f"guest={item['guest_median_ms']:.3f}ms "
            f"delta={item['delta_ms']:.3f}ms "
            f"p95_delta={item['p95_delta_ms']:.3f}ms "
            f"max_delta={item['max_delta_ms']:.3f}ms "
            f"ratio={item['guest_to_host_ratio']}"
        )
        if "ttfb_delta_ms" in item:
            line += (
                f" ttfb_delta={item['ttfb_delta_ms']:.3f}ms "
                f"ttfb_p95_delta={item['ttfb_p95_delta_ms']:.3f}ms"
            )
        print(line)
PY

echo "microagent-host-worker-probe: report"
printf '%s\n' "$REPORT_JSON"
echo "PASS microagent-host-worker-probe: host worker direct and in-guest execution paths succeeded"
