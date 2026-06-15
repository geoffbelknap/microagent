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
#   5. starts model-paired workspace(s) and measures the same calls in-guest,
#   6. cleans up the workspace(s) and the runner it started.
#
# Required:
#   A host model runner resolvable by microagent. For the built-in runner this
#   means llama-server on PATH or MICROAGENT_LLAMA_SERVER set. Custom runners can
#   be supplied through MICROAGENT_MODEL_RUNNER_COMMAND and related env vars.
#
# Optional:
#   MICROAGENT_CLI                         microagent CLI (default: .build/dev/microagent)
#   MICROAGENT_FIRECRACKER                 Firecracker binary for Linux runs
#   MICROAGENT_HOST_WORKER_URL             existing OpenAI-compatible base URL; skips pull/serve
#   MICROAGENT_HOST_WORKER_HEALTH_URL      optional health URL for an existing worker
#   MICROAGENT_HOST_WORKER_MODEL           request model id; auto-discovered from /models when unset
#   MICROAGENT_HOST_WORKER_LABEL           optional report label
#   MICROAGENT_HOST_WORKER_SLOTS           optional report annotation for runner slots/parallelism
#   MICROAGENT_HOST_WORKER_PROBE_MODEL_REF HuggingFace GGUF ref
#   MICROAGENT_HOST_WORKER_PROBE_IMAGE     guest image with curl
#   MICROAGENT_HOST_WORKER_PROBE_STATE_DIR state dir (default: ~/.microagent)
#   MICROAGENT_HOST_WORKER_PROBE_SAMPLES   measured samples per endpoint (default: 5)
#   MICROAGENT_HOST_WORKER_PROBE_WARMUPS   warmup calls per endpoint (default: 1)
#   MICROAGENT_HOST_WORKER_PROBE_CONCURRENCY
#                                           comma-separated per-workspace worker counts (default: 1)
#   MICROAGENT_HOST_WORKER_PROBE_HOST_BASELINE
#                                           before or bracket (default: bracket)
#   MICROAGENT_HOST_WORKER_PROBE_WORKSPACES guest workspace count (default: 1)
#   MICROAGENT_HOST_WORKER_PROBE_KEEP_FAILED
#                                           preserve failed workspaces for inspection: 0/1 (default: 0)
#   MICROAGENT_HOST_WORKER_PROBE_CHAT_PROFILE
#                                           tiny or sustained (default: tiny)
#   MICROAGENT_HOST_WORKER_PROBE_CHAT_TOKENS
#                                           max tokens for non-streaming chat (default: 16)
#   MICROAGENT_HOST_WORKER_PROBE_STREAM     measure streaming chat too: 0/1 (default: 0)
#   MICROAGENT_HOST_WORKER_PROBE_STREAM_TOKENS
#                                           max tokens for streaming chat (default: 128)
#   MICROAGENT_HOST_WORKER_PROBE_GPU_TELEMETRY
#                                           off, auto, or required (default: auto)
#   MICROAGENT_HOST_WORKER_PROBE_GPU_TELEMETRY_INTERVAL
#                                           sampling interval in seconds (default: 0.5)
#   MICROAGENT_HOST_WORKER_PROBE_GPU_TELEMETRY_PATH
#                                           path to write GPU telemetry CSV
#   MICROAGENT_HOST_WORKER_PROBE_NVIDIA_SMI nvidia-smi path override
#   MICROAGENT_HOST_WORKER_PROBE_RUNNER_TELEMETRY
#                                           off, auto, or required (default: auto)
#   MICROAGENT_HOST_WORKER_PROBE_RUNNER_TELEMETRY_INTERVAL
#                                           sampling interval in seconds (default: 0.5)
#   MICROAGENT_HOST_WORKER_PROBE_RUNNER_TELEMETRY_PATH
#                                           path to write runner telemetry JSONL
#   MICROAGENT_HOST_WORKER_PROBE_RUNNER_TELEMETRY_ENDPOINTS
#                                           comma-separated runner diagnostic paths (default: /metrics,/slots)
#   MICROAGENT_HOST_WORKER_PROBE_RUNNER_TELEMETRY_TIMEOUT
#                                           per-endpoint sample timeout in seconds (default: 2)
#   MICROAGENT_HOST_WORKER_PROBE_WORKSPACE workspace name
#   MICROAGENT_HOST_WORKER_PROBE_REPORT    path to write final JSON report
#   MICROAGENT_HOST_WORKER_PROBE_PRINT_REPORT
#                                           print final JSON to stdout: 0/1 (default: 1)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

CLI="${MICROAGENT_CLI:-$(e2e_exe "$ROOT/.build/dev/microagent")}"
HOST_WORKER_URL="${MICROAGENT_HOST_WORKER_URL:-}"
HOST_WORKER_HEALTH_URL="${MICROAGENT_HOST_WORKER_HEALTH_URL:-}"
REQUEST_MODEL="${MICROAGENT_HOST_WORKER_MODEL:-${MICROAGENT_HOST_WORKER_PROBE_REQUEST_MODEL:-}}"
RUN_LABEL="${MICROAGENT_HOST_WORKER_LABEL:-${MICROAGENT_HOST_WORKER_PROBE_LABEL:-}}"
RUNNER_SLOTS="${MICROAGENT_HOST_WORKER_SLOTS:-${MICROAGENT_HOST_WORKER_PROBE_RUNNER_SLOTS:-}}"
MODEL_REF="${MICROAGENT_HOST_WORKER_PROBE_MODEL_REF:-Qwen/Qwen2.5-0.5B-Instruct-GGUF/qwen2.5-0.5b-instruct-q4_k_m.gguf}"
IMAGE="${MICROAGENT_HOST_WORKER_PROBE_IMAGE:-docker.io/curlimages/curl:latest}"
STATE_DIR="${MICROAGENT_HOST_WORKER_PROBE_STATE_DIR:-$HOME/.microagent}"
SAMPLES="${MICROAGENT_HOST_WORKER_PROBE_SAMPLES:-5}"
WARMUPS="${MICROAGENT_HOST_WORKER_PROBE_WARMUPS:-1}"
CONCURRENCY="${MICROAGENT_HOST_WORKER_PROBE_CONCURRENCY:-1}"
HOST_BASELINE="${MICROAGENT_HOST_WORKER_PROBE_HOST_BASELINE:-bracket}"
WORKSPACE_COUNT="${MICROAGENT_HOST_WORKER_PROBE_WORKSPACES:-1}"
KEEP_FAILED="${MICROAGENT_HOST_WORKER_PROBE_KEEP_FAILED:-0}"
CHAT_PROFILE="${MICROAGENT_HOST_WORKER_PROBE_CHAT_PROFILE:-tiny}"
CHAT_TOKENS="${MICROAGENT_HOST_WORKER_PROBE_CHAT_TOKENS:-16}"
STREAM="${MICROAGENT_HOST_WORKER_PROBE_STREAM:-0}"
STREAM_TOKENS="${MICROAGENT_HOST_WORKER_PROBE_STREAM_TOKENS:-128}"
GPU_TELEMETRY="${MICROAGENT_HOST_WORKER_PROBE_GPU_TELEMETRY:-auto}"
GPU_TELEMETRY_INTERVAL="${MICROAGENT_HOST_WORKER_PROBE_GPU_TELEMETRY_INTERVAL:-0.5}"
GPU_TELEMETRY_PATH="${MICROAGENT_HOST_WORKER_PROBE_GPU_TELEMETRY_PATH:-}"
NVIDIA_SMI="${MICROAGENT_HOST_WORKER_PROBE_NVIDIA_SMI:-}"
RUNNER_TELEMETRY="${MICROAGENT_HOST_WORKER_PROBE_RUNNER_TELEMETRY:-auto}"
RUNNER_TELEMETRY_INTERVAL="${MICROAGENT_HOST_WORKER_PROBE_RUNNER_TELEMETRY_INTERVAL:-0.5}"
RUNNER_TELEMETRY_PATH="${MICROAGENT_HOST_WORKER_PROBE_RUNNER_TELEMETRY_PATH:-}"
RUNNER_TELEMETRY_ENDPOINTS="${MICROAGENT_HOST_WORKER_PROBE_RUNNER_TELEMETRY_ENDPOINTS:-/metrics,/slots}"
RUNNER_TELEMETRY_TIMEOUT="${MICROAGENT_HOST_WORKER_PROBE_RUNNER_TELEMETRY_TIMEOUT:-2}"
WS_BASE="${MICROAGENT_HOST_WORKER_PROBE_WORKSPACE:-host-worker-probe-$$}"
REPORT_PATH="${MICROAGENT_HOST_WORKER_PROBE_REPORT:-}"
PRINT_REPORT="${MICROAGENT_HOST_WORKER_PROBE_PRINT_REPORT:-1}"
MODEL_GUEST_PORT=11434
MODEL_VSOCK_PORT=62100
STARTED_RUNNER=0
GPU_TELEMETRY_ACTIVE=0
GPU_TELEMETRY_PID=""
GPU_TELEMETRY_PHASE_FILE=""
GPU_TELEMETRY_TMPDIR=""
GPU_TELEMETRY_PATH_PERSISTED=0
RUNNER_TELEMETRY_ACTIVE=0
RUNNER_TELEMETRY_PID=""
RUNNER_TELEMETRY_PHASE_FILE=""
RUNNER_TELEMETRY_TMPDIR=""
RUNNER_TELEMETRY_PATH_PERSISTED=0
CREATE_FLAGS=()
START_FLAGS=()
CTRL_FLAGS=()
WORKSPACE_NAMES=("$WS_BASE")

skip() { e2e_skip "microagent-host-worker-probe: $1"; }
fail() { echo "FAIL microagent-host-worker-probe: $1" >&2; exit 1; }

stop_gpu_telemetry() {
  if [ -n "$GPU_TELEMETRY_PID" ]; then
    kill "$GPU_TELEMETRY_PID" >/dev/null 2>&1 || true
    wait "$GPU_TELEMETRY_PID" >/dev/null 2>&1 || true
    GPU_TELEMETRY_PID=""
  fi
}

stop_runner_telemetry() {
  if [ -n "$RUNNER_TELEMETRY_PID" ]; then
    kill "$RUNNER_TELEMETRY_PID" >/dev/null 2>&1 || true
    wait "$RUNNER_TELEMETRY_PID" >/dev/null 2>&1 || true
    RUNNER_TELEMETRY_PID=""
  fi
}

cleanup_gpu_telemetry_files() {
  if [ -n "$GPU_TELEMETRY_PHASE_FILE" ]; then
    rm -f "$GPU_TELEMETRY_PHASE_FILE"
    GPU_TELEMETRY_PHASE_FILE=""
  fi
  if [ -n "$GPU_TELEMETRY_TMPDIR" ]; then
    rm -rf "$GPU_TELEMETRY_TMPDIR"
    GPU_TELEMETRY_TMPDIR=""
  fi
}

cleanup_runner_telemetry_files() {
  if [ -n "$RUNNER_TELEMETRY_PHASE_FILE" ]; then
    rm -f "$RUNNER_TELEMETRY_PHASE_FILE"
    RUNNER_TELEMETRY_PHASE_FILE=""
  fi
  if [ -n "$RUNNER_TELEMETRY_TMPDIR" ]; then
    rm -rf "$RUNNER_TELEMETRY_TMPDIR"
    RUNNER_TELEMETRY_TMPDIR=""
  fi
}

cleanup() {
  local status=$?
  set +e
  stop_gpu_telemetry
  stop_runner_telemetry
  cleanup_gpu_telemetry_files
  cleanup_runner_telemetry_files
  if [ "$status" -ne 0 ] && [ "$KEEP_FAILED" -eq 1 ]; then
    echo "microagent-host-worker-probe: preserving failed workspace state: ${WORKSPACE_NAMES[*]}" >&2
  else
    for workspace in "${WORKSPACE_NAMES[@]}"; do
      "$CLI" kill "$workspace" --state-dir "$STATE_DIR" "${CTRL_FLAGS[@]}" >/dev/null 2>&1
      "$CLI" delete "$workspace" --force --yes --state-dir "$STATE_DIR" "${CTRL_FLAGS[@]}" >/dev/null 2>&1
    done
  fi
  if [ "$STARTED_RUNNER" -eq 1 ]; then
    "$CLI" model stop "$MODEL_REF" --state-dir "$STATE_DIR" >/dev/null 2>&1
  fi
  exit "$status"
}
trap cleanup EXIT

build_workspace_names() {
  local i
  WORKSPACE_NAMES=()
  if [ "$WORKSPACE_COUNT" -eq 1 ]; then
    WORKSPACE_NAMES=("$WS_BASE")
    return
  fi
  i=1
  while [ "$i" -le "$WORKSPACE_COUNT" ]; do
    WORKSPACE_NAMES+=("$WS_BASE-$i")
    i=$((i + 1))
  done
}

resolve_nvidia_smi() {
  if [ -n "$NVIDIA_SMI" ]; then
    return
  fi
  if command -v nvidia-smi >/dev/null 2>&1; then
    NVIDIA_SMI="$(command -v nvidia-smi)"
    return
  fi
  if [ -x /usr/lib/wsl/lib/nvidia-smi ]; then
    NVIDIA_SMI=/usr/lib/wsl/lib/nvidia-smi
  fi
}

gpu_telemetry_query_fields() {
  printf '%s\n' 'timestamp,index,utilization.gpu,utilization.memory,memory.used,memory.total,power.draw,pstate,clocks.current.sm,clocks.current.memory,temperature.gpu'
}

gpu_telemetry_available() {
  resolve_nvidia_smi
  if [ -z "$NVIDIA_SMI" ] || [ ! -x "$NVIDIA_SMI" ]; then
    return 1
  fi
  "$NVIDIA_SMI" --query-gpu="$(gpu_telemetry_query_fields)" --format=csv,noheader,nounits >/dev/null 2>&1
}

set_telemetry_phase() {
  local phase="$1"
  if [ "$GPU_TELEMETRY_ACTIVE" -eq 1 ] && [ -n "$GPU_TELEMETRY_PHASE_FILE" ]; then
    printf '%s\n' "$phase" >"$GPU_TELEMETRY_PHASE_FILE"
  fi
  if [ "$RUNNER_TELEMETRY_ACTIVE" -eq 1 ] && [ -n "$RUNNER_TELEMETRY_PHASE_FILE" ]; then
    printf '%s\n' "$phase" >"$RUNNER_TELEMETRY_PHASE_FILE"
  fi
}

start_gpu_telemetry() {
  case "$GPU_TELEMETRY" in
    off)
      GPU_TELEMETRY_ACTIVE=0
      return
      ;;
    auto|required)
      ;;
    *)
      fail "MICROAGENT_HOST_WORKER_PROBE_GPU_TELEMETRY must be off, auto, or required"
      ;;
  esac

  if ! gpu_telemetry_available; then
    if [ "$GPU_TELEMETRY" = "required" ]; then
      fail "GPU telemetry required but nvidia-smi query is unavailable"
    fi
    echo "microagent-host-worker-probe: GPU telemetry unavailable; continuing without it"
    GPU_TELEMETRY_ACTIVE=0
    return
  fi

  if [ -n "$GPU_TELEMETRY_PATH" ]; then
    GPU_TELEMETRY_PATH_PERSISTED=1
  elif [ -n "$REPORT_PATH" ]; then
    case "$REPORT_PATH" in
      *.json) GPU_TELEMETRY_PATH="${REPORT_PATH%.json}.gpu.csv" ;;
      *) GPU_TELEMETRY_PATH="$REPORT_PATH.gpu.csv" ;;
    esac
    GPU_TELEMETRY_PATH_PERSISTED=1
  else
    GPU_TELEMETRY_TMPDIR="$(mktemp -d)"
    GPU_TELEMETRY_PATH="$GPU_TELEMETRY_TMPDIR/gpu.csv"
    GPU_TELEMETRY_PATH_PERSISTED=0
  fi
  mkdir -p "$(dirname "$GPU_TELEMETRY_PATH")"
  GPU_TELEMETRY_PHASE_FILE="$(mktemp)"
  printf '%s\n' startup >"$GPU_TELEMETRY_PHASE_FILE"
  printf '%s\n' 'host_epoch,phase,nvidia_timestamp,gpu_index,gpu_util_pct,memory_util_pct,memory_used_mib,memory_total_mib,power_draw_w,pstate,sm_clock_mhz,memory_clock_mhz,temperature_c' >"$GPU_TELEMETRY_PATH"
  GPU_TELEMETRY_ACTIVE=1

  (
    while :; do
      phase="$(cat "$GPU_TELEMETRY_PHASE_FILE" 2>/dev/null || printf '%s' unknown)"
      sample="$("$NVIDIA_SMI" --query-gpu="$(gpu_telemetry_query_fields)" --format=csv,noheader,nounits 2>/dev/null || true)"
      if [ -n "$sample" ]; then
        while IFS= read -r sample_line; do
          [ -n "$sample_line" ] || continue
          printf '%s,%s,%s\n' "$(date +%s.%N)" "$phase" "$sample_line" >>"$GPU_TELEMETRY_PATH"
        done <<EOF
$sample
EOF
      fi
      sleep "$GPU_TELEMETRY_INTERVAL"
    done
  ) &
  GPU_TELEMETRY_PID="$!"
  echo "microagent-host-worker-probe: GPU telemetry writing to $GPU_TELEMETRY_PATH"
}

add_gpu_telemetry_to_report() {
  local report_json="$1"
  python3 - "$report_json" "$GPU_TELEMETRY_ACTIVE" "$GPU_TELEMETRY_PATH" "$GPU_TELEMETRY_PATH_PERSISTED" <<'PY'
import csv
import json
import statistics
import sys
from pathlib import Path

report = json.loads(sys.argv[1])
active = sys.argv[2] == "1"
path = Path(sys.argv[3]) if sys.argv[3] else None
path_persisted = sys.argv[4] == "1"
telemetry = {
    "enabled": active,
    "path": str(path) if path and path_persisted else None,
    "sample_count": 0,
    "phases": {},
}

def number(value):
    text = str(value).strip()
    if not text or text.upper() in {"N/A", "[NOT SUPPORTED]", "NOT SUPPORTED"}:
        return None
    try:
        return float(text)
    except ValueError:
        return None

def summarize(values):
    clean = sorted(value for value in values if value is not None)
    if not clean:
        return None
    p95_index = min(len(clean) - 1, max(0, int(len(clean) * 0.95 + 0.999999) - 1))
    return {
        "min": clean[0],
        "median": round(statistics.median(clean), 3),
        "mean": round(statistics.fmean(clean), 3),
        "p95": clean[p95_index],
        "max": clean[-1],
    }

if path and path.exists():
    rows = list(csv.DictReader(path.open()))
    telemetry["sample_count"] = len(rows)
    telemetry["gpu_indices"] = sorted({row.get("gpu_index", "").strip() for row in rows if row.get("gpu_index", "").strip()})
    fields = {
        "gpu_util_pct": "gpu_util_pct",
        "memory_util_pct": "memory_util_pct",
        "memory_used_mib": "memory_used_mib",
        "power_draw_w": "power_draw_w",
        "sm_clock_mhz": "sm_clock_mhz",
        "memory_clock_mhz": "memory_clock_mhz",
        "temperature_c": "temperature_c",
    }
    phases = {}
    for row in rows:
        phases.setdefault(row.get("phase") or "unknown", []).append(row)
    for phase, phase_rows in phases.items():
        phase_summary = {"sample_count": len(phase_rows)}
        for source, dest in fields.items():
            stats = summarize(number(row.get(source)) for row in phase_rows)
            if stats is not None:
                phase_summary[dest] = stats
        pstates = sorted({row.get("pstate", "").strip() for row in phase_rows if row.get("pstate", "").strip()})
        if pstates:
            phase_summary["pstates"] = pstates
        telemetry["phases"][phase] = phase_summary

report.setdefault("telemetry", {})["gpu"] = telemetry
print(json.dumps(report, indent=2, sort_keys=True))
PY
}

runner_telemetry_available() {
  local root_url="$1"
  python3 - "$root_url" "$RUNNER_TELEMETRY_ENDPOINTS" "$RUNNER_TELEMETRY_TIMEOUT" <<'PY'
import sys
import urllib.error
import urllib.parse
import urllib.request

root_url = sys.argv[1].rstrip("/")
endpoints = [part.strip() for part in sys.argv[2].replace(" ", ",").split(",") if part.strip()]
timeout = float(sys.argv[3])

for endpoint in endpoints:
    path = endpoint if endpoint.startswith("/") else "/" + endpoint
    url = urllib.parse.urljoin(root_url + "/", path.lstrip("/"))
    try:
        with urllib.request.urlopen(url, timeout=timeout) as response:
            if 200 <= response.status < 300:
                raise SystemExit(0)
    except (OSError, urllib.error.URLError, TimeoutError):
        continue

raise SystemExit(1)
PY
}

start_runner_telemetry() {
  local root_url="$1"
  case "$RUNNER_TELEMETRY" in
    off)
      RUNNER_TELEMETRY_ACTIVE=0
      return
      ;;
    auto|required)
      ;;
    *)
      fail "MICROAGENT_HOST_WORKER_PROBE_RUNNER_TELEMETRY must be off, auto, or required"
      ;;
  esac

  if ! runner_telemetry_available "$root_url"; then
    if [ "$RUNNER_TELEMETRY" = "required" ]; then
      fail "runner telemetry required but no configured runner diagnostic endpoint is available"
    fi
    echo "microagent-host-worker-probe: runner telemetry unavailable; continuing without it"
    RUNNER_TELEMETRY_ACTIVE=0
    return
  fi

  if [ -n "$RUNNER_TELEMETRY_PATH" ]; then
    RUNNER_TELEMETRY_PATH_PERSISTED=1
  elif [ -n "$REPORT_PATH" ]; then
    case "$REPORT_PATH" in
      *.json) RUNNER_TELEMETRY_PATH="${REPORT_PATH%.json}.runner.jsonl" ;;
      *) RUNNER_TELEMETRY_PATH="$REPORT_PATH.runner.jsonl" ;;
    esac
    RUNNER_TELEMETRY_PATH_PERSISTED=1
  else
    RUNNER_TELEMETRY_TMPDIR="$(mktemp -d)"
    RUNNER_TELEMETRY_PATH="$RUNNER_TELEMETRY_TMPDIR/runner.jsonl"
    RUNNER_TELEMETRY_PATH_PERSISTED=0
  fi

  mkdir -p "$(dirname "$RUNNER_TELEMETRY_PATH")"
  : >"$RUNNER_TELEMETRY_PATH"
  RUNNER_TELEMETRY_PHASE_FILE="$(mktemp)"
  printf '%s\n' startup >"$RUNNER_TELEMETRY_PHASE_FILE"
  RUNNER_TELEMETRY_ACTIVE=1

  python3 - "$root_url" "$RUNNER_TELEMETRY_PATH" "$RUNNER_TELEMETRY_PHASE_FILE" "$RUNNER_TELEMETRY_INTERVAL" "$RUNNER_TELEMETRY_TIMEOUT" "$RUNNER_TELEMETRY_ENDPOINTS" <<'PY' &
import json
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

root_url = sys.argv[1].rstrip("/")
out_path = Path(sys.argv[2])
phase_path = Path(sys.argv[3])
interval = float(sys.argv[4])
timeout = float(sys.argv[5])
endpoints = [part.strip() for part in sys.argv[6].replace(" ", ",").split(",") if part.strip()]
metric_pattern = re.compile(r"^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{[^}]*\})?\s+(-?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?)$")
interesting_metric = re.compile(r"(llama|slot|queue|request|prompt|token|eval|cache|kv|batch|busy|process)", re.IGNORECASE)
active_states = {"active", "busy", "processing", "generating", "decode", "prefill", "queued", "pending"}
bool_active_keys = ("is_processing", "processing", "active", "busy", "is_busy", "is_generating", "has_task")
numeric_slot_keys = (
    "n_ctx",
    "n_past",
    "n_prompt_tokens",
    "n_decoded",
    "n_remaining",
    "n_predict",
    "n_tokens",
    "n_cache_tokens",
    "prompt_tokens",
    "completion_tokens",
    "tokens",
)

def endpoint_url(endpoint):
    path = endpoint if endpoint.startswith("/") else "/" + endpoint
    return urllib.parse.urljoin(root_url + "/", path.lstrip("/"))

def numeric(value):
    if isinstance(value, bool):
        return None
    if isinstance(value, (int, float)):
        return float(value)
    return None

def item_active(item):
    for key in bool_active_keys:
        if item.get(key) is True:
            return True
    state = str(item.get("state") or item.get("status") or "").strip().lower()
    if state in active_states:
        return True
    return False

def summarize_items(items):
    signals = {
        "json_items": len(items),
        "slot_count": len(items),
    }
    active_count = sum(1 for item in items if item_active(item))
    signals["active_slot_count"] = active_count
    signals["idle_slot_count"] = max(0, len(items) - active_count)
    for key in numeric_slot_keys:
        values = [numeric(item.get(key)) for item in items if isinstance(item, dict)]
        values = [value for value in values if value is not None]
        if values:
            signals[f"{key}_sum"] = round(sum(values), 3)
            signals[f"{key}_max"] = round(max(values), 3)
    return signals

def json_signals(doc):
    if isinstance(doc, list):
        dict_items = [item for item in doc if isinstance(item, dict)]
        return summarize_items(dict_items)
    if isinstance(doc, dict):
        for key in ("slots", "data", "items"):
            value = doc.get(key)
            if isinstance(value, list):
                signals = summarize_items([item for item in value if isinstance(item, dict)])
                signals["json_object_keys"] = len(doc)
                return signals
        signals = {"json_object_keys": len(doc)}
        for key, value in doc.items():
            value_num = numeric(value)
            if value_num is not None:
                signals[f"json_{key}"] = value_num
        return signals
    return {}

def prometheus_signals(text):
    totals = {}
    series_counts = {}
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        match = metric_pattern.match(line)
        if not match:
            continue
        name = match.group(1)
        if name.endswith("_bucket") or not interesting_metric.search(name):
            continue
        try:
            value = float(match.group(2))
        except ValueError:
            continue
        totals[name] = totals.get(name, 0.0) + value
        series_counts[name] = series_counts.get(name, 0) + 1
    signals = {"metric_series": sum(series_counts.values())}
    for name in sorted(totals)[:80]:
        signals[f"metric_{name}_sum"] = round(totals[name], 6)
        signals[f"metric_{name}_series"] = series_counts[name]
    return signals

def summarize_body(content_type, body):
    text = body.decode("utf-8", errors="replace")
    stripped = text.lstrip()
    if "json" in content_type.lower() or stripped.startswith(("[", "{")):
        try:
            return "json", json_signals(json.loads(text))
        except json.JSONDecodeError:
            pass
    return "prometheus", prometheus_signals(text)

def read_phase():
    try:
        phase = phase_path.read_text().strip()
        return phase or "unknown"
    except OSError:
        return "unknown"

def sample_endpoint(endpoint):
    url = endpoint_url(endpoint)
    start_epoch = time.time()
    start = time.perf_counter()
    sample = {
        "host_epoch": round(start_epoch, 6),
        "phase": read_phase(),
        "endpoint": endpoint if endpoint.startswith("/") else "/" + endpoint,
        "url": url,
        "ok": False,
    }
    try:
        with urllib.request.urlopen(url, timeout=timeout) as response:
            body = response.read(1024 * 1024 + 1)
            elapsed_ms = (time.perf_counter() - start) * 1000
            content_type = response.headers.get("Content-Type", "")
            sample.update({
                "ok": 200 <= response.status < 300,
                "status": response.status,
                "elapsed_ms": round(elapsed_ms, 3),
                "content_type": content_type,
                "body_bytes": len(body),
                "body_truncated": len(body) > 1024 * 1024,
            })
            body_format, signals = summarize_body(content_type, body[:1024 * 1024])
            sample["format"] = body_format
            sample["signals"] = signals
    except urllib.error.HTTPError as err:
        body = err.read(1024 * 1024 + 1)
        content_type = err.headers.get("Content-Type", "") if err.headers else ""
        sample.update({
            "elapsed_ms": round((time.perf_counter() - start) * 1000, 3),
            "status": err.code,
            "content_type": content_type,
            "body_bytes": len(body),
            "body_truncated": len(body) > 1024 * 1024,
            "error": str(err),
        })
        if body:
            body_format, signals = summarize_body(content_type, body[:1024 * 1024])
            sample["format"] = body_format
            sample["signals"] = signals
    except (OSError, urllib.error.URLError, TimeoutError) as err:
        sample.update({
            "elapsed_ms": round((time.perf_counter() - start) * 1000, 3),
            "error": str(err),
        })
    return sample

with out_path.open("a", encoding="utf-8") as out:
    while True:
        loop_start = time.perf_counter()
        for endpoint in endpoints:
            out.write(json.dumps(sample_endpoint(endpoint), sort_keys=True) + "\n")
        out.flush()
        remaining = interval - (time.perf_counter() - loop_start)
        time.sleep(max(0.05, remaining))
PY
  RUNNER_TELEMETRY_PID="$!"
  echo "microagent-host-worker-probe: runner telemetry writing to $RUNNER_TELEMETRY_PATH"
}

add_runner_telemetry_to_report() {
  local report_json="$1"
  python3 - "$report_json" "$RUNNER_TELEMETRY_ACTIVE" "$RUNNER_TELEMETRY_PATH" "$RUNNER_TELEMETRY_PATH_PERSISTED" <<'PY'
import json
import statistics
import sys
from pathlib import Path

report = json.loads(sys.argv[1])
active = sys.argv[2] == "1"
path = Path(sys.argv[3]) if sys.argv[3] else None
path_persisted = sys.argv[4] == "1"
telemetry = {
    "enabled": active,
    "path": str(path) if path and path_persisted else None,
    "sample_count": 0,
    "phases": {},
}

def summarize(values):
    clean = sorted(value for value in values if isinstance(value, (int, float)))
    if not clean:
        return None
    p95_index = min(len(clean) - 1, max(0, int(len(clean) * 0.95 + 0.999999) - 1))
    return {
        "min": clean[0],
        "median": round(statistics.median(clean), 3),
        "mean": round(statistics.fmean(clean), 3),
        "p95": clean[p95_index],
        "max": clean[-1],
    }

if path and path.exists():
    rows = []
    for line in path.read_text().splitlines():
        if not line.strip():
            continue
        try:
            rows.append(json.loads(line))
        except json.JSONDecodeError:
            continue
    telemetry["sample_count"] = len(rows)
    telemetry["endpoints"] = sorted({row.get("endpoint") for row in rows if row.get("endpoint")})
    by_phase = {}
    for row in rows:
        phase = row.get("phase") or "unknown"
        endpoint = row.get("endpoint") or "unknown"
        by_phase.setdefault(phase, {}).setdefault(endpoint, []).append(row)
    for phase, endpoints in by_phase.items():
        phase_out = {}
        for endpoint, endpoint_rows in endpoints.items():
            endpoint_out = {
                "sample_count": len(endpoint_rows),
                "ok_count": sum(1 for row in endpoint_rows if row.get("ok") is True),
                "status_codes": sorted({str(row.get("status")) for row in endpoint_rows if row.get("status") is not None}),
                "formats": sorted({row.get("format") for row in endpoint_rows if row.get("format")}),
            }
            elapsed = summarize([row.get("elapsed_ms") for row in endpoint_rows])
            if elapsed is not None:
                endpoint_out["elapsed_ms"] = elapsed
            signal_values = {}
            for row in endpoint_rows:
                for key, value in (row.get("signals") or {}).items():
                    if isinstance(value, (int, float)):
                        signal_values.setdefault(key, []).append(value)
            signals_out = {}
            for key in sorted(signal_values)[:120]:
                stats = summarize(signal_values[key])
                if stats is not None:
                    signals_out[key] = stats
            if signals_out:
                endpoint_out["signals"] = signals_out
            errors = [row.get("error") for row in endpoint_rows if row.get("error")]
            if errors:
                endpoint_out["last_error"] = errors[-1]
            phase_out[endpoint] = endpoint_out
        telemetry["phases"][phase] = phase_out

report.setdefault("telemetry", {})["runner"] = telemetry
print(json.dumps(report, indent=2, sort_keys=True))
PY
}

add_pressure_summary_to_report() {
  local report_json="$1"
  python3 - "$report_json" <<'PY'
import json
import sys

report = json.loads(sys.argv[1])

PRESSURE_SCOPE = (
    "descriptive telemetry only; the model runner owns request scheduling, "
    "batching, KV cache management, and GPU execution"
)

RUNNING_CANDIDATES = (
    ("runner", "/slots", "active_slot_count"),
    ("runner", "/metrics", "metric_llamacpp:requests_processing_sum"),
    ("runner", "/metrics", "metric_vllm:num_requests_running_sum"),
    ("runner", "/metrics", "metric_vllm:num_requests_processing_sum"),
    ("runner", "/metrics", "json_num_requests_running"),
)
SLOT_COUNT_CANDIDATES = (
    ("runner", "/slots", "slot_count"),
    ("runner", "/slots", "json_items"),
)
WAITING_CANDIDATES = (
    ("runner", "/metrics", "metric_vllm:num_requests_waiting_sum"),
    ("runner", "/metrics", "metric_vllm:num_requests_waiting_by_reason_sum"),
    ("runner", "/metrics", "json_num_requests_waiting"),
)
DEFERRED_CANDIDATES = (
    ("runner", "/metrics", "metric_llamacpp:requests_deferred_sum"),
    ("runner", "/metrics", "metric_vllm:num_requests_waiting_by_reason_sum"),
    ("runner", "/metrics", "json_num_requests_deferred"),
    ("runner", "/metrics", "json_num_skipped_waiting_reqs"),
)
KV_USAGE_CANDIDATES = (
    ("runner", "/metrics", "metric_vllm:kv_cache_usage_perc_sum"),
    ("runner", "/metrics", "json_kv_cache_usage"),
    ("runner", "/metrics", "json_kv_cache_usage_perc"),
)


def stat_at(telemetry, phase, candidates):
    phase_doc = telemetry.get("phases", {}).get(phase, {})
    for _kind, endpoint, key in candidates:
        endpoint_doc = phase_doc.get(endpoint, {})
        signals = endpoint_doc.get("signals", {})
        value = signals.get(key)
        if isinstance(value, dict):
            return {
                "source": f"{endpoint} signals.{key}",
                "min": value.get("min"),
                "median": value.get("median"),
                "mean": value.get("mean"),
                "p95": value.get("p95"),
                "max": value.get("max"),
            }
    return None


def phase_gpu(gpu_telemetry, phase):
    phase_doc = gpu_telemetry.get("phases", {}).get(phase, {})
    util = phase_doc.get("gpu_util_pct")
    if not isinstance(util, dict):
        return None
    out = {
        "gpu_util_pct": {
            "median": util.get("median"),
            "p95": util.get("p95"),
            "max": util.get("max"),
        },
        "sample_count": phase_doc.get("sample_count"),
    }
    power = phase_doc.get("power_draw_w")
    if isinstance(power, dict):
        out["power_draw_w"] = {
            "median": power.get("median"),
            "p95": power.get("p95"),
            "max": power.get("max"),
        }
    return out


def classify_gpu(gpu):
    if not gpu:
        return "unavailable"
    util = gpu.get("gpu_util_pct") or {}
    median = util.get("median")
    p95 = util.get("p95")
    if median is None and p95 is None:
        return "unavailable"
    if (median is not None and median >= 85) or (p95 is not None and p95 >= 95):
        return "high"
    if (median is not None and median >= 50) or (p95 is not None and p95 >= 75):
        return "moderate"
    return "low"


def active_fraction(active, slots):
    if not active or not slots:
        return None
    active_median = active.get("median")
    slot_median = slots.get("median")
    if active_median is None or slot_median is None or slot_median == 0:
        return None
    return round(active_median / slot_median, 3)


def classify_runner(active, slots, waiting, deferred):
    waiting_max = (waiting or {}).get("max")
    deferred_max = (deferred or {}).get("max")
    if waiting_max and waiting_max > 0:
        return "waiting_observed"
    if deferred_max and deferred_max > 0:
        return "deferred_observed"
    fraction = active_fraction(active, slots)
    if fraction is None:
        if active:
            return "active_observed"
        return "unavailable"
    if fraction >= 0.95:
        return "slots_saturated"
    if fraction >= 0.5:
        return "slots_busy"
    return "slots_available"


def endpoint_latency(level_doc, endpoint):
    guest = level_doc.get("guest", {}).get(endpoint, {})
    overhead = level_doc.get("overhead", {}).get(endpoint, {})
    if not guest:
        return None
    out = {
        "guest_median_ms": guest.get("median_ms"),
        "guest_p95_ms": guest.get("p95_ms"),
        "guest_to_host_delta_ms": overhead.get("delta_ms"),
        "guest_to_host_p95_delta_ms": overhead.get("p95_delta_ms"),
    }
    if "ttfb_median_ms" in guest:
        out["guest_ttfb_median_ms"] = guest.get("ttfb_median_ms")
        out["guest_to_host_ttfb_delta_ms"] = overhead.get("ttfb_delta_ms")
    return out


def summary_sentence(runner_state, gpu_state):
    if runner_state in {"waiting_observed", "deferred_observed"}:
        return "runner reported queued or deferred work"
    if runner_state == "slots_saturated" and gpu_state in {"low", "moderate"}:
        return "runner slots were saturated before GPU telemetry looked saturated"
    if runner_state == "slots_busy" and gpu_state in {"low", "moderate"}:
        return "runner slots were busy without clear GPU saturation"
    if runner_state in {"slots_busy", "slots_saturated"} and gpu_state == "high":
        return "runner and GPU both showed high pressure"
    if runner_state == "slots_available" and gpu_state in {"low", "moderate"}:
        return "no clear runner or GPU saturation in sampled telemetry"
    if runner_state == "unavailable" and gpu_state == "unavailable":
        return "pressure telemetry unavailable"
    return "pressure source is inconclusive from sampled telemetry"


telemetry = report.get("telemetry", {})
runner_telemetry = telemetry.get("runner") or {}
gpu_telemetry = telemetry.get("gpu") or {}

pressure = {
    "scope": PRESSURE_SCOPE,
    "schema_version": 1,
    "levels": {},
}

for level in report.get("concurrency_levels", []):
    level_key = str(level)
    phase = f"guest:c={level}"
    level_doc = report.get("matrix", {}).get(level_key, {})
    active = stat_at(runner_telemetry, phase, RUNNING_CANDIDATES)
    slots = stat_at(runner_telemetry, phase, SLOT_COUNT_CANDIDATES)
    waiting = stat_at(runner_telemetry, phase, WAITING_CANDIDATES)
    deferred = stat_at(runner_telemetry, phase, DEFERRED_CANDIDATES)
    kv_usage = stat_at(runner_telemetry, phase, KV_USAGE_CANDIDATES)
    gpu = phase_gpu(gpu_telemetry, phase)
    runner_state = classify_runner(active, slots, waiting, deferred)
    gpu_state = classify_gpu(gpu)
    effective = None
    for endpoint_doc in level_doc.get("guest", {}).values():
        if isinstance(endpoint_doc, dict) and endpoint_doc.get("concurrency"):
            effective = endpoint_doc.get("concurrency")
            break

    runner_out = {
        "active_requests": active,
        "slot_count": slots,
        "active_slot_fraction_median": active_fraction(active, slots),
        "waiting_requests": waiting,
        "deferred_requests": deferred,
        "kv_cache_usage": kv_usage,
    }
    pressure["levels"][level_key] = {
        "phase": phase,
        "per_workspace_concurrency": level,
        "effective_concurrency": effective,
        "runner": {key: value for key, value in runner_out.items() if value is not None},
        "gpu": gpu,
        "latency": {
            endpoint: latency
            for endpoint in ("chat", "stream")
            if (latency := endpoint_latency(level_doc, endpoint)) is not None
        },
        "classification": {
            "runner": runner_state,
            "gpu": gpu_state,
            "summary": summary_sentence(runner_state, gpu_state),
        },
    }

report["pressure"] = pressure
print(json.dumps(report, indent=2, sort_keys=True))
PY
}

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

host_worker_url_info() {
  local raw_url="$1"
  python3 - "$raw_url" <<'PY'
import json
import sys
import urllib.parse

raw_url = sys.argv[1].strip()
if not raw_url:
    raise SystemExit("MICROAGENT_HOST_WORKER_URL must not be empty")
if "://" not in raw_url:
    raw_url = "http://" + raw_url
raw_url = raw_url.rstrip("/")
parsed = urllib.parse.urlparse(raw_url)
if parsed.scheme != "http":
    raise SystemExit("MICROAGENT_HOST_WORKER_URL must use http://; the guest bridge is a plain TCP forward")
if parsed.username or parsed.password:
    raise SystemExit("MICROAGENT_HOST_WORKER_URL must not include credentials")
if parsed.query or parsed.fragment or parsed.params:
    raise SystemExit("MICROAGENT_HOST_WORKER_URL must not include query, fragment, or path parameters")
if not parsed.hostname:
    raise SystemExit("MICROAGENT_HOST_WORKER_URL must include a host")
try:
    port = parsed.port or 80
except ValueError as err:
    raise SystemExit(f"invalid MICROAGENT_HOST_WORKER_URL port: {err}") from err

path = parsed.path.rstrip("/")
if not path:
    path = "/v1"
netloc = parsed.netloc
base_url = urllib.parse.urlunparse((parsed.scheme, netloc, path, "", "", ""))
if path.endswith("/v1"):
    root_path = path[:-3].rstrip("/")
else:
    root_path = ""
root_url = urllib.parse.urlunparse((parsed.scheme, netloc, root_path, "", "", "")).rstrip("/")
if not root_url:
    root_url = f"{parsed.scheme}://{netloc}"

target_host = parsed.hostname
if ":" in target_host and not target_host.startswith("["):
    target_host = f"[{target_host}]"

print(json.dumps({
    "base_url": base_url,
    "base_path": path,
    "host": parsed.hostname,
    "port": port,
    "root_url": root_url,
    "scheme": parsed.scheme,
    "target": f"{target_host}:{port}",
}, separators=(",", ":"), sort_keys=True))
PY
}

host_worker_health_check() {
  local base_url="$1"
  local health_url="$2"
  python3 - "$base_url" "$health_url" <<'PY'
import sys
import urllib.error
import urllib.request

base_url = sys.argv[1].rstrip("/")
health_url = sys.argv[2].strip() or f"{base_url}/models"
try:
    with urllib.request.urlopen(health_url, timeout=10) as response:
        response.read(1024 * 1024)
        if not 200 <= response.status < 300:
            raise SystemExit(f"{health_url} returned HTTP {response.status}")
except urllib.error.HTTPError as err:
    raise SystemExit(f"{health_url} returned HTTP {err.code}") from err
except (OSError, urllib.error.URLError, TimeoutError) as err:
    raise SystemExit(f"{health_url} is not reachable: {err}") from err
PY
}

discover_request_model() {
  local base_url="$1"
  python3 - "$base_url" <<'PY'
import json
import sys
import urllib.error
import urllib.request

base_url = sys.argv[1].rstrip("/")
try:
    with urllib.request.urlopen(f"{base_url}/models", timeout=10) as response:
        doc = json.loads(response.read(1024 * 1024))
except (OSError, urllib.error.URLError, TimeoutError, json.JSONDecodeError):
    raise SystemExit(1)

models = doc.get("data") if isinstance(doc, dict) else None
if not isinstance(models, list):
    raise SystemExit(1)
for model in models:
    if isinstance(model, dict) and model.get("id"):
        print(model["id"])
        raise SystemExit(0)
raise SystemExit(1)
PY
}

external_runner_json() {
  local info_json="$1"
  python3 - "$info_json" <<'PY'
import json
import sys

info = json.loads(sys.argv[1])
print(json.dumps({
    "base_url_path": info["base_path"],
    "engine": "openai-compatible",
    "host": info["host"],
    "mode": "external",
    "model_ref": "external-host-worker",
    "pid": None,
    "port": info["port"],
    "runner_config_digest": "",
    "scheme": info["scheme"],
}, separators=(",", ":"), sort_keys=True))
PY
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
  python3 - "$base_url" "$SAMPLES" "$WARMUPS" "$CONCURRENCY" "$WORKSPACE_COUNT" "$CHAT_PROFILE" "$CHAT_TOKENS" "$STREAM" "$STREAM_TOKENS" "$REQUEST_MODEL" <<'PY'
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
workspace_count = int(sys.argv[5])
chat_profile = sys.argv[6]
chat_tokens = int(sys.argv[7])
stream_enabled = sys.argv[8] == "1"
stream_tokens = int(sys.argv[9])
request_model = sys.argv[10]
timeout = 180

if samples <= 0:
    raise SystemExit("samples must be > 0")
if warmups < 0:
    raise SystemExit("warmups must be >= 0")
if not levels or any(level <= 0 for level in levels):
    raise SystemExit("concurrency levels must be positive integers")
if workspace_count <= 0:
    raise SystemExit("workspace count must be > 0")
if chat_profile not in ("tiny", "sustained"):
    raise SystemExit("chat profile must be tiny or sustained")
if chat_tokens <= 0:
    raise SystemExit("chat tokens must be > 0")
if stream_tokens <= 0:
    raise SystemExit("stream tokens must be > 0")
total = warmups + samples

def open_and_read(req, max_bytes):
    start_epoch = time.time()
    start = time.perf_counter()
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        first = resp.read(1)
        if not first:
            raise RuntimeError("response body was empty")
        ttfb_ms = (time.perf_counter() - start) * 1000
        body_read_start = time.perf_counter()
        body = first + resp.read(max_bytes)
        body_read_ms = (time.perf_counter() - body_read_start) * 1000
    elapsed_ms = (time.perf_counter() - start) * 1000
    return body, {
        "elapsed_ms": round(elapsed_ms, 3),
        "ttfb_ms": round(ttfb_ms, 3),
        "body_read_ms": round(body_read_ms, 3),
        "response_bytes": len(body),
        "start_epoch": round(start_epoch, 6),
        "end_epoch": round(time.time(), 6),
    }

def request_models():
    req = urllib.request.Request(base_url + "/models", method="GET")
    body, timing = open_and_read(req, 4 * 1024 * 1024)
    doc = json.loads(body)
    if "object" not in doc and "data" not in doc:
        raise RuntimeError("/models response did not look OpenAI-compatible")
    return timing

def request_chat():
    if chat_profile == "sustained":
        content = "Write one compact paragraph about mediated host GPU workers."
    else:
        content = "Reply with exactly: PONG"
    payload_doc = {
        "messages": [{"role": "user", "content": content}],
        "max_tokens": chat_tokens,
        "temperature": 0,
    }
    if request_model:
        payload_doc["model"] = request_model
    payload = json.dumps(payload_doc).encode("utf-8")
    req = urllib.request.Request(
        base_url + "/chat/completions",
        data=payload,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    body, timing = open_and_read(req, 4 * 1024 * 1024)
    doc = json.loads(body)
    if not doc.get("choices"):
        raise RuntimeError("/chat/completions response did not contain choices")
    return timing

def request_stream():
    payload_doc = {
        "messages": [
            {
                "role": "user",
                "content": "Write one compact paragraph about mediated host GPU workers.",
            }
        ],
        "max_tokens": stream_tokens,
        "temperature": 0,
        "stream": True,
    }
    if request_model:
        payload_doc["model"] = request_model
    payload = json.dumps(payload_doc).encode("utf-8")
    req = urllib.request.Request(
        base_url + "/chat/completions",
        data=payload,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    body, timing = open_and_read(req, 64 * 1024 * 1024)
    if b"[DONE]" not in body and b'"choices"' not in body:
        raise RuntimeError("stream response did not look OpenAI-compatible")
    timing.update({
        "bytes": len(body),
        "chunks": sum(1 for line in body.splitlines() if line.startswith(b"data:")),
    })
    return timing

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

def summarize(samples_for_endpoint, concurrency, per_workspace_concurrency):
    elapsed_values = [item["elapsed_ms"] for item in samples_for_endpoint]
    ordered = sorted(elapsed_values)
    p95_index = min(len(ordered) - 1, max(0, int(len(ordered) * 0.95 + 0.999999) - 1))
    summary = {
        "concurrency": concurrency,
        "per_workspace_concurrency": per_workspace_concurrency,
        "samples_per_worker": samples,
        "warmups_per_worker": warmups,
        "workspace_count": workspace_count,
        "sample_count": len(elapsed_values),
        "samples_ms": elapsed_values,
        "min_ms": ordered[0],
        "median_ms": round(statistics.median(ordered), 3),
        "mean_ms": round(statistics.fmean(ordered), 3),
        "p95_ms": ordered[p95_index],
        "max_ms": ordered[-1],
    }
    start_epochs = [item["start_epoch"] for item in samples_for_endpoint if "start_epoch" in item]
    end_epochs = [item["end_epoch"] for item in samples_for_endpoint if "end_epoch" in item]
    if start_epochs and end_epochs:
        summary.update({
            "first_start_epoch": min(start_epochs),
            "last_end_epoch": max(end_epochs),
            "wall_span_ms": round((max(end_epochs) - min(start_epochs)) * 1000, 3),
        })
    optional_metrics = (
        ("ttfb_ms", "ttfb", "ms"),
        ("body_read_ms", "body_read", "ms"),
        ("response_bytes", "response_bytes", ""),
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

def run_level(fn, per_workspace_concurrency):
    concurrency = per_workspace_concurrency * workspace_count
    values = []
    barrier = threading.Barrier(concurrency)
    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
        futures = [pool.submit(worker, fn, barrier) for _ in range(concurrency)]
        for future in concurrent.futures.as_completed(futures):
            values.extend(future.result())
    return summarize(values, concurrency, per_workspace_concurrency)

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

annotate_host_benchmark() {
  local host_json="$1"
  local baseline="$2"
  python3 - "$host_json" "$baseline" <<'PY'
import json
import sys

report = json.loads(sys.argv[1])
baseline = sys.argv[2]
report["host_baseline"] = baseline
report["host_baseline_passes"] = [baseline]
print(json.dumps(report, sort_keys=True))
PY
}

merge_host_benchmarks() {
  local before_json="$1"
  local after_json="$2"
  python3 - "$before_json" "$after_json" <<'PY'
import json
import statistics
import sys

before = json.loads(sys.argv[1])
after = json.loads(sys.argv[2])

def endpoint_order(keys):
    preferred = ["models", "chat", "stream"]
    present = set(keys)
    return [key for key in preferred if key in present] + sorted(present - set(preferred))

def summarize(values):
    clean = [round(float(value), 3) for value in values]
    ordered = sorted(clean)
    p95_index = min(len(ordered) - 1, max(0, int(len(ordered) * 0.95 + 0.999999) - 1))
    return {
        "sample_count": len(clean),
        "samples_ms": clean,
        "min_ms": ordered[0],
        "median_ms": round(statistics.median(ordered), 3),
        "mean_ms": round(statistics.fmean(ordered), 3),
        "p95_ms": ordered[p95_index],
        "max_ms": ordered[-1],
    }

def summarize_number(values, name, unit):
    clean = [round(float(value), 3) for value in values]
    ordered = sorted(clean)
    p95_index = min(len(ordered) - 1, max(0, int(len(ordered) * 0.95 + 0.999999) - 1))
    unit_suffix = f"_{unit}" if unit else ""
    return {
        f"{name}_samples{unit_suffix}": clean,
        f"{name}_min{unit_suffix}": ordered[0],
        f"{name}_median{unit_suffix}": round(statistics.median(ordered), 3),
        f"{name}_mean{unit_suffix}": round(statistics.fmean(ordered), 3),
        f"{name}_p95{unit_suffix}": ordered[p95_index],
        f"{name}_max{unit_suffix}": ordered[-1],
    }

def merged_endpoint(level, endpoint):
    before_doc = before["levels"][level][endpoint]
    after_doc = after["levels"][level][endpoint]
    samples = list(before_doc.get("samples_ms") or []) + list(after_doc.get("samples_ms") or [])
    if not samples:
        raise SystemExit(f"host baseline {level}/{endpoint} had no samples")
    out = summarize(samples)
    for key in ("concurrency", "per_workspace_concurrency", "samples_per_worker", "warmups_per_worker", "workspace_count"):
        if key in before_doc:
            out[key] = before_doc[key]
    start_epochs = [
        value
        for value in (before_doc.get("first_start_epoch"), after_doc.get("first_start_epoch"))
        if value is not None
    ]
    end_epochs = [
        value
        for value in (before_doc.get("last_end_epoch"), after_doc.get("last_end_epoch"))
        if value is not None
    ]
    if start_epochs and end_epochs:
        out["first_start_epoch"] = min(start_epochs)
        out["last_end_epoch"] = max(end_epochs)
        spans = [
            value
            for value in (before_doc.get("wall_span_ms"), after_doc.get("wall_span_ms"))
            if value is not None
        ]
        if spans:
            out["wall_span_ms"] = round(sum(float(value) for value in spans), 3)
    optional_metrics = (
        ("ttfb_samples_ms", "ttfb", "ms"),
        ("body_read_samples_ms", "body_read", "ms"),
        ("response_bytes_samples", "response_bytes", ""),
        ("bytes_samples", "bytes", ""),
        ("chunks_samples", "chunks", ""),
    )
    for sample_key, name, unit in optional_metrics:
        values = list(before_doc.get(sample_key) or []) + list(after_doc.get(sample_key) or [])
        if values:
            out.update(summarize_number(values, name, unit))
    out["host_baseline"] = "bracket"
    out["host_baseline_passes"] = ["before", "after"]
    out["host_baseline_sample_counts"] = {
        "before": before_doc.get("sample_count"),
        "after": after_doc.get("sample_count"),
    }
    return out

levels = sorted(set(before.get("levels", {})) & set(after.get("levels", {})), key=lambda value: int(value))
merged = {
    "host_baseline": "bracket",
    "host_baseline_passes": ["before", "after"],
    "levels": {},
}
for level in levels:
    before_level = before["levels"][level]
    after_level = after["levels"][level]
    endpoints = endpoint_order(set(before_level) & set(after_level))
    merged["levels"][level] = {
        endpoint: merged_endpoint(level, endpoint)
        for endpoint in endpoints
    }

print(json.dumps(merged, sort_keys=True))
PY
}

combine_report() {
  local host_json="$1"
  local guest_json="$2"
  local backend="$3"
  local canonical="$4"
  local runner_json="$5"
  python3 - "$host_json" "$guest_json" "$backend" "$canonical" "$runner_json" "$WORKSPACE_COUNT" "$CHAT_PROFILE" "$CHAT_TOKENS" "$STREAM" "$STREAM_TOKENS" "$REQUEST_MODEL" "$RUN_LABEL" "$RUNNER_SLOTS" <<'PY'
import json
import statistics
import sys

host_raw = json.loads(sys.argv[1])
guest_raw = json.loads(sys.argv[2])
backend = sys.argv[3]
canonical = sys.argv[4]
runner = json.loads(sys.argv[5])
workspace_count = int(sys.argv[6])
chat_profile = sys.argv[7]
chat_tokens = int(sys.argv[8])
stream_enabled = sys.argv[9] == "1"
stream_tokens = int(sys.argv[10])
request_model = sys.argv[11] or None
run_label = sys.argv[12] or None
runner_slots_raw = sys.argv[13] or None

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
        values = [float(value) for value in data["samples_ms"]]
    summary = summarize(values)
    for key in ("first_start_epoch", "last_end_epoch", "wall_span_ms"):
        if key in data:
            summary[key] = data[key]
    ttfb_values_ms = None
    if "ttfb_samples_seconds" in data:
        ttfb_values_ms = [float(value) * 1000 for value in data["ttfb_samples_seconds"]]
        summary.update(summarize_number(ttfb_values_ms, "ttfb", "ms"))
    elif "ttfb_samples_ms" in data:
        ttfb_values_ms = [float(value) for value in data["ttfb_samples_ms"]]
        summary.update(summarize_number(ttfb_values_ms, "ttfb", "ms"))
    if "connect_samples_seconds" in data:
        summary.update(summarize_number([float(value) * 1000 for value in data["connect_samples_seconds"]], "connect", "ms"))
    if "pretransfer_samples_seconds" in data:
        summary.update(summarize_number([float(value) * 1000 for value in data["pretransfer_samples_seconds"]], "pretransfer", "ms"))
    body_read_values_ms = None
    if "body_read_samples_ms" in data:
        body_read_values_ms = [float(value) for value in data["body_read_samples_ms"]]
    elif "body_read_samples_seconds" in data:
        body_read_values_ms = [float(value) * 1000 for value in data["body_read_samples_seconds"]]
    elif ttfb_values_ms is not None and len(ttfb_values_ms) == len(values):
        body_read_values_ms = [max(0.0, elapsed - ttfb) for elapsed, ttfb in zip(values, ttfb_values_ms)]
    if body_read_values_ms is not None:
        summary.update(summarize_number(body_read_values_ms, "body_read", "ms"))
    if "response_bytes_samples" in data:
        summary.update(summarize_number(data["response_bytes_samples"], "response_bytes", ""))
    chunk_values = None
    for key, name in (("bytes_samples", "bytes"), ("chunks_samples", "chunks")):
        if key in data:
            summary.update(summarize_number(data[key], name, ""))
            if key == "chunks_samples":
                chunk_values = [float(value) for value in data[key]]
    if body_read_values_ms is not None and chunk_values is not None and len(body_read_values_ms) == len(chunk_values):
        per_chunk = [
            body_ms / chunks
            for body_ms, chunks in zip(body_read_values_ms, chunk_values)
            if chunks > 0
        ]
        per_chunk_gap = [
            body_ms / (chunks - 1)
            for body_ms, chunks in zip(body_read_values_ms, chunk_values)
            if chunks > 1
        ]
        if per_chunk:
            summary.update(summarize_number(per_chunk, "body_read_per_chunk", "ms"))
        if per_chunk_gap:
            summary.update(summarize_number(per_chunk_gap, "body_read_per_chunk_gap", "ms"))
    return summary

def normalize(report):
    if "levels" not in report:
        report = {"levels": {"1": report}}
    report_workspace_count = int(report.get("workspace_count") or workspace_count)
    normalized = {"levels": {}}
    for level, level_report in report["levels"].items():
        normalized["levels"][str(level)] = {}
        for endpoint in endpoint_order(level_report.keys()):
            data = level_report[endpoint]
            summary = endpoint_summary(data)
            summary["workspace_count"] = int(data.get("workspace_count") or report_workspace_count)
            summary["per_workspace_concurrency"] = int(data.get("per_workspace_concurrency") or level)
            summary["concurrency"] = int(data.get("concurrency") or (summary["per_workspace_concurrency"] * summary["workspace_count"]))
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
    if "body_read_median_ms" in host_summary and "body_read_median_ms" in guest_summary:
        result.update({
            "host_body_read_median_ms": host_summary["body_read_median_ms"],
            "host_body_read_p95_ms": host_summary["body_read_p95_ms"],
            "guest_body_read_median_ms": guest_summary["body_read_median_ms"],
            "guest_body_read_p95_ms": guest_summary["body_read_p95_ms"],
            "body_read_delta_ms": round(guest_summary["body_read_median_ms"] - host_summary["body_read_median_ms"], 3),
            "body_read_p95_delta_ms": round(guest_summary["body_read_p95_ms"] - host_summary["body_read_p95_ms"], 3),
        })
    if "body_read_per_chunk_median_ms" in host_summary and "body_read_per_chunk_median_ms" in guest_summary:
        result.update({
            "host_body_read_per_chunk_median_ms": host_summary["body_read_per_chunk_median_ms"],
            "guest_body_read_per_chunk_median_ms": guest_summary["body_read_per_chunk_median_ms"],
            "body_read_per_chunk_delta_ms": round(guest_summary["body_read_per_chunk_median_ms"] - host_summary["body_read_per_chunk_median_ms"], 3),
        })
    if "body_read_per_chunk_gap_median_ms" in host_summary and "body_read_per_chunk_gap_median_ms" in guest_summary:
        result.update({
            "host_body_read_per_chunk_gap_median_ms": host_summary["body_read_per_chunk_gap_median_ms"],
            "guest_body_read_per_chunk_gap_median_ms": guest_summary["body_read_per_chunk_gap_median_ms"],
            "body_read_per_chunk_gap_delta_ms": round(guest_summary["body_read_per_chunk_gap_median_ms"] - host_summary["body_read_per_chunk_gap_median_ms"], 3),
        })
    return result

levels = sorted(host["levels"].keys(), key=lambda value: int(value))
endpoints = endpoint_order(host["levels"][levels[0]].keys())
workspace_names = guest_raw.get("workspaces") or []
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
for key in ("mode", "scheme", "base_url_path"):
    if runner.get(key) is not None:
        public_runner[key] = runner.get(key)
report = {
    "backend": backend,
    "concurrency_levels": [int(level) for level in levels],
    "endpoints": endpoints,
    "effective_concurrency_levels": [
        matrix[level]["guest"]["models"].get("concurrency", int(level) * workspace_count)
        for level in levels
        if "models" in matrix[level]["guest"]
    ],
    "matrix": matrix,
    "model_ref": canonical,
    "measurement_design": {
        "host_baseline": host_raw.get("host_baseline", "before"),
        "host_baseline_passes": host_raw.get("host_baseline_passes", ["before"]),
    },
    "per_workspace_concurrency_levels": [int(level) for level in levels],
    "request_profiles": {
        "chat": {
            "profile": chat_profile,
            "max_tokens": chat_tokens,
            "model": request_model,
            "stream": False,
        },
        "stream": {
            "enabled": stream_enabled,
            "max_tokens": stream_tokens,
            "model": request_model,
        },
    },
    "runner": public_runner,
    "samples_per_worker": matrix[levels[0]]["host"]["models"].get("samples_per_worker", matrix[levels[0]]["host"]["models"].get("sample_count", 0) // int(levels[0])),
    "warmups_per_worker": matrix[levels[0]]["host"]["models"].get("warmups_per_worker"),
    "workspace_count": workspace_count,
    "workspaces": workspace_names,
}
experiment = {}
if run_label:
    experiment["label"] = run_label
if runner_slots_raw:
    try:
        experiment["runner_slots"] = int(runner_slots_raw)
    except ValueError:
        experiment["runner_slots"] = runner_slots_raw
if experiment:
    report["experiment"] = experiment
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
case "$WORKSPACE_COUNT" in
  ''|*[!0-9]*) fail "MICROAGENT_HOST_WORKER_PROBE_WORKSPACES must be a positive integer" ;;
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
if [ "$WORKSPACE_COUNT" -le 0 ]; then
  fail "MICROAGENT_HOST_WORKER_PROBE_WORKSPACES must be > 0"
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
case "$GPU_TELEMETRY" in
  off|auto|required)
    ;;
  *)
    fail "MICROAGENT_HOST_WORKER_PROBE_GPU_TELEMETRY must be off, auto, or required"
    ;;
esac
case "$RUNNER_TELEMETRY" in
  off|auto|required)
    ;;
  *)
    fail "MICROAGENT_HOST_WORKER_PROBE_RUNNER_TELEMETRY must be off, auto, or required"
    ;;
esac
case "$HOST_BASELINE" in
  before|bracket)
    ;;
  *)
    fail "MICROAGENT_HOST_WORKER_PROBE_HOST_BASELINE must be before or bracket"
    ;;
esac
case "$KEEP_FAILED" in
  1|true|TRUE|yes|YES)
    KEEP_FAILED=1
    ;;
  0|false|FALSE|no|NO)
    KEEP_FAILED=0
    ;;
  *)
    fail "MICROAGENT_HOST_WORKER_PROBE_KEEP_FAILED must be 0/1, true/false, or yes/no"
    ;;
esac
case "$PRINT_REPORT" in
  1|true|TRUE|yes|YES)
    PRINT_REPORT=1
    ;;
  0|false|FALSE|no|NO)
    PRINT_REPORT=0
    ;;
  *)
    fail "MICROAGENT_HOST_WORKER_PROBE_PRINT_REPORT must be 0/1, true/false, or yes/no"
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
build_workspace_names

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

RUNNER_MODE=managed
if [ -n "$HOST_WORKER_URL" ]; then
  RUNNER_MODE=external
fi
echo "microagent-host-worker-probe: backend=$BACKEND runner_mode=$RUNNER_MODE model=$MODEL_REF image=$IMAGE samples=$SAMPLES warmups=$WARMUPS workspaces=$WORKSPACE_COUNT concurrency=$CONCURRENCY host_baseline=$HOST_BASELINE chat_profile=$CHAT_PROFILE chat_tokens=$CHAT_TOKENS stream=$STREAM stream_tokens=$STREAM_TOKENS gpu_telemetry=$GPU_TELEMETRY runner_telemetry=$RUNNER_TELEMETRY"

GUEST_MODEL_URL="http://127.0.0.1:$MODEL_GUEST_PORT/v1"
if [ "$RUNNER_MODE" = "external" ]; then
  HOST_WORKER_INFO="$(host_worker_url_info "$HOST_WORKER_URL")" || fail "invalid MICROAGENT_HOST_WORKER_URL"
  RUNNER_BASE_URL="$(printf '%s' "$HOST_WORKER_INFO" | json_get base_url)"
  RUNNER_ROOT_URL="$(printf '%s' "$HOST_WORKER_INFO" | json_get root_url)"
  HOST_WORKER_TARGET="$(printf '%s' "$HOST_WORKER_INFO" | json_get target)"
  HOST_WORKER_BASE_PATH="$(printf '%s' "$HOST_WORKER_INFO" | json_get base_path)"
  GUEST_MODEL_URL="http://127.0.0.1:$MODEL_GUEST_PORT$HOST_WORKER_BASE_PATH"
  CANONICAL=external-host-worker
  RUNNER_JSON="$(external_runner_json "$HOST_WORKER_INFO")"
  echo "microagent-host-worker-probe: using existing host worker at $RUNNER_BASE_URL"
  health_err=""
  if ! health_err="$(host_worker_health_check "$RUNNER_BASE_URL" "$HOST_WORKER_HEALTH_URL" 2>&1)"; then
    fail "external host worker health check failed: $health_err"
  fi
else
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
  RUNNER_ROOT_URL="http://$RUNNER_HOST:$RUNNER_PORT"
  RUNNER_BASE_URL="$RUNNER_ROOT_URL/v1"
fi

if [ -z "$REQUEST_MODEL" ]; then
  discovered_model=""
  if discovered_model="$(discover_request_model "$RUNNER_BASE_URL" 2>/dev/null)"; then
    REQUEST_MODEL="$discovered_model"
    echo "microagent-host-worker-probe: using request model $REQUEST_MODEL"
  else
    echo "microagent-host-worker-probe: no request model discovered; chat payloads will omit model"
  fi
else
  echo "microagent-host-worker-probe: using configured request model $REQUEST_MODEL"
fi

start_gpu_telemetry
start_runner_telemetry "$RUNNER_ROOT_URL"
set_telemetry_phase host-direct
echo "microagent-host-worker-probe: measuring direct host calls before guest run at $RUNNER_BASE_URL"
HOST_BEFORE_JSON="$(host_benchmark "$RUNNER_BASE_URL")" || fail "direct host benchmark failed"
HOST_JSON="$(annotate_host_benchmark "$HOST_BEFORE_JSON" before)"

set_telemetry_phase workspace-start
for workspace in "${WORKSPACE_NAMES[@]}"; do
  "$CLI" delete "$workspace" --force --yes --state-dir "$STATE_DIR" "${CTRL_FLAGS[@]}" >/dev/null 2>&1 || true
done
echo "microagent-host-worker-probe: creating and starting $WORKSPACE_COUNT paired workspace(s)"
for workspace in "${WORKSPACE_NAMES[@]}"; do
  echo "microagent-host-worker-probe: starting paired workspace $workspace"
  if [ "$RUNNER_MODE" = "external" ]; then
    "$CLI" create "$workspace" \
      --image "$IMAGE" \
      --state-dir "$STATE_DIR" \
      --mediation "$MODEL_VSOCK_PORT=$HOST_WORKER_TARGET" \
      --env "MICROAGENT_VSOCK_TCP_LISTENERS=127.0.0.1:$MODEL_GUEST_PORT=$MODEL_VSOCK_PORT" \
      --env "MICROAGENT_MODEL_URL=$GUEST_MODEL_URL" \
      --env "OPENAI_BASE_URL=$GUEST_MODEL_URL" \
      "${CREATE_FLAGS[@]}" >/dev/null || fail "create external worker bridge failed for $workspace"
  else
    "$CLI" create "$workspace" --image "$IMAGE" --model "$MODEL_REF" --state-dir "$STATE_DIR" "${CREATE_FLAGS[@]}" >/dev/null || fail "create --model failed for $workspace"
  fi
  "$CLI" start "$workspace" --state-dir "$STATE_DIR" "${START_FLAGS[@]}" >/dev/null || fail "start failed for $workspace"
  if ! e2e_wait_exec_ready "$CLI" "$STATE_DIR" "$workspace" 90; then
    "$CLI" --json status "$workspace" --state-dir "$STATE_DIR" "${CTRL_FLAGS[@]}" >&2 || true
    fail "workspace exec service did not become ready for $workspace"
  fi
done

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

json_escape() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

request_model_prefix() {
  if [ -n "${PROBE_REQUEST_MODEL:-}" ]; then
    printf '"model":"%s",' "$(json_escape "$PROBE_REQUEST_MODEL")"
  fi
}

measure_models_worker() {
  out="$1"
  ttfb_out="$2"
  connect_out="$3"
  pretransfer_out="$4"
  bytes_out="$5"
  i=0
  total=$((PROBE_WARMUPS + PROBE_SAMPLES))
  while [ "$i" -lt "$total" ]; do
    body="$(mktemp)"
    metrics="$(curl -sS -o "$body" -w '%{time_connect} %{time_pretransfer} %{time_starttransfer} %{time_total} %{size_download}' "$MICROAGENT_MODEL_URL/models")"
    set -- $metrics
    connect="$1"
    pretransfer="$2"
    ttfb="$3"
    elapsed="$4"
    bytes="$5"
    if ! grep -Eq '"object"|"data"' "$body"; then
      echo "models response did not look OpenAI-compatible" >&2
      cat "$body" >&2
      rm -f "$body"
      exit 1
    fi
    rm -f "$body"
    if [ "$i" -ge "$PROBE_WARMUPS" ]; then
      printf '%s\n' "$elapsed" >>"$out"
      printf '%s\n' "$ttfb" >>"$ttfb_out"
      printf '%s\n' "$connect" >>"$connect_out"
      printf '%s\n' "$pretransfer" >>"$pretransfer_out"
      printf '%s\n' "$bytes" >>"$bytes_out"
    fi
    i=$((i + 1))
  done
}

measure_chat_worker() {
  out="$1"
  ttfb_out="$2"
  connect_out="$3"
  pretransfer_out="$4"
  bytes_out="$5"
  model_prefix="$(request_model_prefix)"
  if [ "$PROBE_CHAT_PROFILE" = "sustained" ]; then
    payload='{'"$model_prefix"'"messages":[{"role":"user","content":"Write one compact paragraph about mediated host GPU workers."}],"max_tokens":'"$PROBE_CHAT_TOKENS"',"temperature":0}'
  else
    payload='{'"$model_prefix"'"messages":[{"role":"user","content":"Reply with exactly: PONG"}],"max_tokens":'"$PROBE_CHAT_TOKENS"',"temperature":0}'
  fi
  i=0
  total=$((PROBE_WARMUPS + PROBE_SAMPLES))
  while [ "$i" -lt "$total" ]; do
    body="$(mktemp)"
    metrics="$(curl -sS -o "$body" -w '%{time_connect} %{time_pretransfer} %{time_starttransfer} %{time_total} %{size_download}' "$MICROAGENT_MODEL_URL/chat/completions" -H "Content-Type: application/json" -d "$payload")"
    set -- $metrics
    connect="$1"
    pretransfer="$2"
    ttfb="$3"
    elapsed="$4"
    bytes="$5"
    if ! grep -q '"choices"' "$body"; then
      echo "chat response did not contain choices" >&2
      cat "$body" >&2
      rm -f "$body"
      exit 1
    fi
    rm -f "$body"
    if [ "$i" -ge "$PROBE_WARMUPS" ]; then
      printf '%s\n' "$elapsed" >>"$out"
      printf '%s\n' "$ttfb" >>"$ttfb_out"
      printf '%s\n' "$connect" >>"$connect_out"
      printf '%s\n' "$pretransfer" >>"$pretransfer_out"
      printf '%s\n' "$bytes" >>"$bytes_out"
    fi
    i=$((i + 1))
  done
}

measure_stream_worker() {
  out="$1"
  ttfb_out="$2"
  bytes_out="$3"
  chunks_out="$4"
  connect_out="$5"
  pretransfer_out="$6"
  model_prefix="$(request_model_prefix)"
  payload='{'"$model_prefix"'"messages":[{"role":"user","content":"Write one compact paragraph about mediated host GPU workers."}],"max_tokens":'"$PROBE_STREAM_TOKENS"',"temperature":0,"stream":true}'
  i=0
  total=$((PROBE_WARMUPS + PROBE_SAMPLES))
  while [ "$i" -lt "$total" ]; do
    body="$(mktemp)"
    metrics="$(curl -sS -N -o "$body" -w '%{time_connect} %{time_pretransfer} %{time_starttransfer} %{time_total} %{size_download}' "$MICROAGENT_MODEL_URL/chat/completions" -H "Content-Type: application/json" -d "$payload")"
    set -- $metrics
    connect="$1"
    pretransfer="$2"
    ttfb="$3"
    elapsed="$4"
    bytes="$5"
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
      printf '%s\n' "$connect" >>"$connect_out"
      printf '%s\n' "$pretransfer" >>"$pretransfer_out"
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
    measure_models_worker "$tmp/$worker.total" "$tmp/$worker.ttfb" "$tmp/$worker.connect" "$tmp/$worker.pretransfer" "$tmp/$worker.bytes" &
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
  ttfb_file="$tmp/ttfb"
  connect_file="$tmp/connect"
  pretransfer_file="$tmp/pretransfer"
  bytes_file="$tmp/bytes"
  : >"$values_file"
  : >"$ttfb_file"
  : >"$connect_file"
  : >"$pretransfer_file"
  : >"$bytes_file"
  worker=1
  while [ "$worker" -le "$level" ]; do
    cat "$tmp/$worker.total" >>"$values_file"
    cat "$tmp/$worker.ttfb" >>"$ttfb_file"
    cat "$tmp/$worker.connect" >>"$connect_file"
    cat "$tmp/$worker.pretransfer" >>"$pretransfer_file"
    cat "$tmp/$worker.bytes" >>"$bytes_file"
    worker=$((worker + 1))
  done
  printf '"samples_seconds":[%s],"ttfb_samples_seconds":[%s],"connect_samples_seconds":[%s],"pretransfer_samples_seconds":[%s],"bytes_samples":[%s]' "$(join_values "$values_file")" "$(join_values "$ttfb_file")" "$(join_values "$connect_file")" "$(join_values "$pretransfer_file")" "$(join_values "$bytes_file")"
  rm -rf "$tmp"
}

measure_chat() {
  level="$1"
  tmp="$(mktemp -d)"
  pids=""
  worker=1
  while [ "$worker" -le "$level" ]; do
    measure_chat_worker "$tmp/$worker.total" "$tmp/$worker.ttfb" "$tmp/$worker.connect" "$tmp/$worker.pretransfer" "$tmp/$worker.bytes" &
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
  ttfb_file="$tmp/ttfb"
  connect_file="$tmp/connect"
  pretransfer_file="$tmp/pretransfer"
  bytes_file="$tmp/bytes"
  : >"$values_file"
  : >"$ttfb_file"
  : >"$connect_file"
  : >"$pretransfer_file"
  : >"$bytes_file"
  worker=1
  while [ "$worker" -le "$level" ]; do
    cat "$tmp/$worker.total" >>"$values_file"
    cat "$tmp/$worker.ttfb" >>"$ttfb_file"
    cat "$tmp/$worker.connect" >>"$connect_file"
    cat "$tmp/$worker.pretransfer" >>"$pretransfer_file"
    cat "$tmp/$worker.bytes" >>"$bytes_file"
    worker=$((worker + 1))
  done
  printf '"samples_seconds":[%s],"ttfb_samples_seconds":[%s],"connect_samples_seconds":[%s],"pretransfer_samples_seconds":[%s],"bytes_samples":[%s]' "$(join_values "$values_file")" "$(join_values "$ttfb_file")" "$(join_values "$connect_file")" "$(join_values "$pretransfer_file")" "$(join_values "$bytes_file")"
  rm -rf "$tmp"
}

measure_stream() {
  level="$1"
  tmp="$(mktemp -d)"
  pids=""
  worker=1
  while [ "$worker" -le "$level" ]; do
    measure_stream_worker "$tmp/$worker.total" "$tmp/$worker.ttfb" "$tmp/$worker.bytes" "$tmp/$worker.chunks" "$tmp/$worker.connect" "$tmp/$worker.pretransfer" &
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
  connect_file="$tmp/connect"
  pretransfer_file="$tmp/pretransfer"
  : >"$total_file"
  : >"$ttfb_file"
  : >"$bytes_file"
  : >"$chunks_file"
  : >"$connect_file"
  : >"$pretransfer_file"
  worker=1
  while [ "$worker" -le "$level" ]; do
    cat "$tmp/$worker.total" >>"$total_file"
    cat "$tmp/$worker.ttfb" >>"$ttfb_file"
    cat "$tmp/$worker.bytes" >>"$bytes_file"
    cat "$tmp/$worker.chunks" >>"$chunks_file"
    cat "$tmp/$worker.connect" >>"$connect_file"
    cat "$tmp/$worker.pretransfer" >>"$pretransfer_file"
    worker=$((worker + 1))
  done
  printf '"samples_seconds":[%s],"ttfb_samples_seconds":[%s],"connect_samples_seconds":[%s],"pretransfer_samples_seconds":[%s],"bytes_samples":[%s],"chunks_samples":[%s]' "$(join_values "$total_file")" "$(join_values "$ttfb_file")" "$(join_values "$connect_file")" "$(join_values "$pretransfer_file")" "$(join_values "$bytes_file")" "$(join_values "$chunks_file")"
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
  printf '"%s":{"models":{%s,"samples_per_worker":%s,"warmups_per_worker":%s},"chat":{%s,"samples_per_worker":%s,"warmups_per_worker":%s}' "$level" "$models_values" "$PROBE_SAMPLES" "$PROBE_WARMUPS" "$chat_values" "$PROBE_SAMPLES" "$PROBE_WARMUPS"
  if [ "$PROBE_STREAM" = "1" ]; then
    stream_values="$(measure_stream "$level")"
    printf ',"stream":{%s,"samples_per_worker":%s,"warmups_per_worker":%s}' "$stream_values" "$PROBE_SAMPLES" "$PROBE_WARMUPS"
  fi
  printf '}'
done
printf '}}\n'
SH

run_guest_benchmarks() {
  local tmp
  local level
  local workspace
  local idx
  local status
  local pid
  local err
  local -a pids

  tmp="$(mktemp -d)"
  for level in $CONCURRENCY_SPACES; do
    set_telemetry_phase "guest:c=$level"
    echo "microagent-host-worker-probe: measuring in-guest calls at c=$level across $WORKSPACE_COUNT workspace(s)" >&2
    pids=()
    idx=1
    for workspace in "${WORKSPACE_NAMES[@]}"; do
      "$CLI" exec "$workspace" --state-dir "$STATE_DIR" \
        -env "PROBE_SAMPLES=$SAMPLES" \
        -env "PROBE_WARMUPS=$WARMUPS" \
        -env "PROBE_CONCURRENCY=$level" \
        -env "PROBE_CHAT_PROFILE=$CHAT_PROFILE" \
        -env "PROBE_CHAT_TOKENS=$CHAT_TOKENS" \
        -env "PROBE_REQUEST_MODEL=$REQUEST_MODEL" \
        -env "PROBE_STREAM=$STREAM" \
        -env "PROBE_STREAM_TOKENS=$STREAM_TOKENS" \
        -- sh -c "$GUEST_BENCH" >"$tmp/$level-$idx.json" 2>"$tmp/$level-$idx.err" &
      pids+=("$!")
      idx=$((idx + 1))
    done

    status=0
    for pid in "${pids[@]}"; do
      wait "$pid" || status=1
    done
    if [ "$status" -ne 0 ]; then
      for err in "$tmp"/"$level"-*.err; do
        [ ! -s "$err" ] || cat "$err" >&2
      done
      rm -rf "$tmp"
      return 1
    fi
  done

  python3 - "$tmp" "$WORKSPACE_COUNT" "$CONCURRENCY_SPACES" "${WORKSPACE_NAMES[@]}" <<'PY'
import json
import pathlib
import sys

tmp = pathlib.Path(sys.argv[1])
workspace_count = int(sys.argv[2])
levels = sys.argv[3].split()
workspaces = sys.argv[4:]

aggregate = {
    "workspace_count": workspace_count,
    "workspaces": workspaces,
    "levels": {},
}

def endpoint_order(keys):
    preferred = ["models", "chat", "stream"]
    present = set(keys)
    return [key for key in preferred if key in present] + sorted(present - set(preferred))

sample_fields = {
    "samples_seconds",
    "ttfb_samples_seconds",
    "body_read_samples_seconds",
    "connect_samples_seconds",
    "pretransfer_samples_seconds",
    "bytes_samples",
    "chunks_samples",
}

for level in levels:
    level_out = aggregate["levels"].setdefault(level, {})
    for idx, _workspace in enumerate(workspaces, start=1):
        path = tmp / f"{level}-{idx}.json"
        doc = json.loads(path.read_text())
        level_doc = doc["levels"][str(level)]
        for endpoint in endpoint_order(level_doc.keys()):
            data = level_doc[endpoint]
            endpoint_out = level_out.setdefault(endpoint, {
                "samples_per_worker": data.get("samples_per_worker"),
                "warmups_per_worker": data.get("warmups_per_worker"),
                "workspace_count": workspace_count,
                "per_workspace_concurrency": int(level),
                "concurrency": int(level) * workspace_count,
            })
            for field in sample_fields:
                if field in data:
                    endpoint_out.setdefault(field, []).extend(data[field])

print(json.dumps(aggregate, sort_keys=True))
PY
  rm -rf "$tmp"
}

GUEST_JSON="$(run_guest_benchmarks)" || fail "guest benchmark failed"
if [ "$HOST_BASELINE" = "bracket" ]; then
  set_telemetry_phase host-after
  echo "microagent-host-worker-probe: measuring direct host calls after guest run at $RUNNER_BASE_URL"
  HOST_AFTER_JSON="$(host_benchmark "$RUNNER_BASE_URL")" || fail "post-guest direct host benchmark failed"
  HOST_JSON="$(merge_host_benchmarks "$HOST_BEFORE_JSON" "$HOST_AFTER_JSON")"
fi
set_telemetry_phase report
stop_gpu_telemetry
stop_runner_telemetry

REPORT_JSON="$(combine_report "$HOST_JSON" "$GUEST_JSON" "$BACKEND" "$CANONICAL" "$RUNNER_JSON")"
REPORT_JSON="$(add_gpu_telemetry_to_report "$REPORT_JSON")"
REPORT_JSON="$(add_runner_telemetry_to_report "$REPORT_JSON")"
REPORT_JSON="$(add_pressure_summary_to_report "$REPORT_JSON")"
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
        effective = level_report["guest"][endpoint].get("concurrency", level)
        line = (
            "microagent-host-worker-probe: "
            f"workspaces={report.get('workspace_count', 1)} c={level} total_c={effective} "
            f"{endpoint} median host={item['host_median_ms']:.3f}ms "
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
        if "body_read_delta_ms" in item:
            line += (
                f" body_delta={item['body_read_delta_ms']:.3f}ms "
                f"body_p95_delta={item['body_read_p95_delta_ms']:.3f}ms"
            )
        if "body_read_per_chunk_gap_delta_ms" in item:
            line += (
                f" chunk_gap_delta={item['body_read_per_chunk_gap_delta_ms']:.3f}ms"
            )
        print(line)
for level, item in (report.get("pressure", {}).get("levels", {}) or {}).items():
    classification = item.get("classification") or {}
    effective = item.get("effective_concurrency")
    runner = item.get("runner") or {}
    gpu = item.get("gpu") or {}
    latency = item.get("latency") or {}
    active = runner.get("active_requests") or {}
    slots = runner.get("slot_count") or {}
    waiting = runner.get("waiting_requests") or {}
    deferred = runner.get("deferred_requests") or {}
    util = (gpu.get("gpu_util_pct") or {}) if isinstance(gpu, dict) else {}
    power = (gpu.get("power_draw_w") or {}) if isinstance(gpu, dict) else {}
    chat = latency.get("chat") or {}
    stream = latency.get("stream") or {}

    def fmt(value, suffix=""):
        if value is None:
            return "na"
        if isinstance(value, float):
            text = f"{value:.3f}".rstrip("0").rstrip(".")
        else:
            text = str(value)
        return f"{text}{suffix}"

    print(
        "microagent-host-worker-probe: "
        f"pressure c={level} total_c={effective} "
        f"runner={classification.get('runner')} "
        f"active_slots={fmt(active.get('median'))}/{fmt(slots.get('median'))} "
        f"active_frac={fmt(runner.get('active_slot_fraction_median'))} "
        f"waiting_max={fmt(waiting.get('max'))} "
        f"deferred_max={fmt(deferred.get('max'))} "
        f"gpu={classification.get('gpu')} "
        f"gpu_util={fmt(util.get('median'), '%')}/{fmt(util.get('p95'), '%')}/{fmt(util.get('max'), '%')} "
        f"gpu_power={fmt(power.get('median'), 'W')}/{fmt(power.get('p95'), 'W')} "
        f"chat_delta={fmt(chat.get('guest_to_host_delta_ms'), 'ms')} "
        f"stream_delta={fmt(stream.get('guest_to_host_delta_ms'), 'ms')} "
        f"stream_ttfb_delta={fmt(stream.get('guest_to_host_ttfb_delta_ms'), 'ms')} "
        f"summary={classification.get('summary')}"
    )
PY

if [ "$PRINT_REPORT" -eq 1 ]; then
  echo "microagent-host-worker-probe: report"
  printf '%s\n' "$REPORT_JSON"
fi
echo "PASS microagent-host-worker-probe: host worker direct and in-guest execution paths succeeded"
