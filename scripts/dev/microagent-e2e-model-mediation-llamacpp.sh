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
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

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
MAX_MODELS_TOTAL_P95_DELTA_MS="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_MAX_MODELS_TOTAL_P95_DELTA_MS:-100}"
MAX_CHAT_TOTAL_P95_DELTA_MS="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_MAX_CHAT_TOTAL_P95_DELTA_MS:-500}"
MAX_STREAM_TTFB_P95_DELTA_MS="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_MAX_STREAM_TTFB_P95_DELTA_MS:-250}"
MAX_DECISION_P95_MS="${MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_MAX_DECISION_P95_MS:-100}"
POLICY_PID=""
POLICY_URL=""
RUNNER_PID=""
RUNNER_PORT=""
REQUEST_MODEL=""
CANONICAL_REF=""
TELEMETRY_PID=""
TELEMETRY_PHASE_FILE=""
RUN_FLAGS=(--backend firecracker --network isolated --state-dir "$STATE_DIR" --model "$MODEL_REF" --rm "$IMAGE")
CTRL_FLAGS=(--backend firecracker --state-dir "$STATE_DIR")

skip() { e2e_skip "microagent-e2e-model-mediation-llamacpp: $1"; }
fail() { echo "FAIL microagent-e2e-model-mediation-llamacpp: $1" >&2; exit 1; }

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

cleanup() {
  local status=$?
  set +e
  stop_telemetry
  if [ -n "$POLICY_PID" ]; then
    kill "$POLICY_PID" >/dev/null 2>&1 || true
    wait "$POLICY_PID" >/dev/null 2>&1 || true
    POLICY_PID=""
  fi
  for workspace in mml-direct mml-local mml-pa mml-pd mml-pu; do
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
      fail "MICROAGENT_E2E_MODEL_MEDIATION_LLAMA_TELEMETRY must be off, auto, or required"
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
  echo "microagent-e2e-model-mediation-llamacpp: telemetry writing to $OUT_DIR/runner-telemetry.jsonl and $OUT_DIR/gpu-telemetry.csv"
}

write_telemetry_summary() {
  if [ "$TELEMETRY" = "off" ]; then
    return
  fi
  stop_telemetry
  python3 "$ROOT/scripts/dev/microagent-model-mediation-telemetry.py" summary \
    --runner-in "$OUT_DIR/runner-telemetry.jsonl" \
    --gpu-in "$OUT_DIR/gpu-telemetry.csv" \
    --adapter llama.cpp \
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

def p95(values):
    if not values:
        return ""
    values = sorted(values)
    idx = min(len(values) - 1, max(0, int(len(values) * 0.95 + 0.999999) - 1))
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
profile_rows = []
for line in stdout_path.read_text(encoding="utf-8", errors="replace").splitlines():
    if line.startswith("PROFILE\t"):
        profile_rows.append(line.split("\t"))

def ms(raw):
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

def as_float(raw):
    if raw == "":
        return None
    try:
        return float(raw)
    except ValueError:
        return None

def percentile(values, pct):
    if not values:
        return None
    ordered = sorted(values)
    idx = max(0, min(len(ordered) - 1, math.ceil((pct / 100) * len(ordered)) - 1))
    return ordered[idx]

def fmt(value):
    return f"{value:.3f}" if value is not None else ""

grouped = defaultdict(list)
for row in rows:
    grouped[(row["case"], row["endpoint"])].append(row)

summary = {}
for key, group in grouped.items():
    statuses = sorted({row.get("status", "") for row in group})
    totals = [value for row in group if (value := as_float(row.get("total_ms", ""))) is not None]
    ttfbs = [value for row in group if (value := as_float(row.get("ttfb_ms", ""))) is not None]
    summary[key] = {
        "samples": str(len(group)),
        "status": ",".join(statuses),
        "total_p50_ms": fmt(statistics.median(totals) if totals else None),
        "total_p95_ms": fmt(percentile(totals, 95)),
        "ttfb_p50_ms": fmt(statistics.median(ttfbs) if ttfbs else None),
        "ttfb_p95_ms": fmt(percentile(ttfbs, 95)),
    }

with summary_path.open("w", encoding="utf-8", newline="") as handle:
    writer = csv.writer(handle, delimiter="\t")
    writer.writerow(["case", "endpoint", "status", "samples", "total_p50_ms", "total_p95_ms", "ttfb_p50_ms", "ttfb_p95_ms"])
    for case in ["direct", "local", "pa", "pd", "pu"]:
        for endpoint in ["models", "chat", "stream"]:
            data = summary.get((case, endpoint))
            if data:
                writer.writerow([case, endpoint, data["status"], data["samples"], data["total_p50_ms"], data["total_p95_ms"], data["ttfb_p50_ms"], data["ttfb_p95_ms"]])

with comparison_path.open("w", encoding="utf-8", newline="") as handle:
    writer = csv.writer(handle, delimiter="\t")
    writer.writerow([
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
    ])
    for endpoint in ["models", "chat", "stream"]:
        direct = summary.get(("direct", endpoint))
        if not direct:
            continue
        for case in ["local", "pa"]:
            row = summary.get((case, endpoint))
            if not row:
                continue
            values = {
                "direct_total_p50": as_float(direct["total_p50_ms"]),
                "case_total_p50": as_float(row["total_p50_ms"]),
                "direct_total_p95": as_float(direct["total_p95_ms"]),
                "case_total_p95": as_float(row["total_p95_ms"]),
                "direct_ttfb_p50": as_float(direct["ttfb_p50_ms"]),
                "case_ttfb_p50": as_float(row["ttfb_p50_ms"]),
                "direct_ttfb_p95": as_float(direct["ttfb_p95_ms"]),
                "case_ttfb_p95": as_float(row["ttfb_p95_ms"]),
            }
            writer.writerow([
                endpoint,
                case,
                row.get("status", ""),
                fmt(values["direct_total_p50"]),
                fmt(values["case_total_p50"]),
                fmt(values["case_total_p50"] - values["direct_total_p50"]) if values["direct_total_p50"] is not None and values["case_total_p50"] is not None else "",
                fmt(values["direct_total_p95"]),
                fmt(values["case_total_p95"]),
                fmt(values["case_total_p95"] - values["direct_total_p95"]) if values["direct_total_p95"] is not None and values["case_total_p95"] is not None else "",
                fmt(values["direct_ttfb_p50"]),
                fmt(values["case_ttfb_p50"]),
                fmt(values["case_ttfb_p50"] - values["direct_ttfb_p50"]) if values["direct_ttfb_p50"] is not None and values["case_ttfb_p50"] is not None else "",
                fmt(values["direct_ttfb_p95"]),
                fmt(values["case_ttfb_p95"]),
                fmt(values["case_ttfb_p95"] - values["direct_ttfb_p95"]) if values["direct_ttfb_p95"] is not None and values["case_ttfb_p95"] is not None else "",
            ])
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

run_case() {
  local label="$1"
  local mode="$2"
  local expected_status="$3"
  local workspace="mml-$label"
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
stream_payload='{"model":"'"$REQUEST_MODEL"'","messages":[{"role":"user","content":"Write one compact sentence about mediated host model workers."}],"max_tokens":'"$STREAM_TOKENS"',"temperature":0,"stream":true}'

sample=1
while [ "$sample" -le "$SAMPLES" ]; do
  metrics="$(profile_curl /tmp/model-body "$MICROAGENT_MODEL_URL/models")"
  set -- $metrics
  status="${1:-000}"
  printf 'PROFILE\tmodels\t%s\t%s\t%s\t%s\t%s\t\n' "$sample" "$status" "${2:-0}" "${3:-0}" "${4:-0}"
  test "$status" = "$EXPECTED_STATUS"
  if [ "$EXPECTED_STATUS" = "200" ]; then
    grep -q "$REQUEST_MODEL" /tmp/model-body
  fi
  sample=$((sample + 1))
done

sample=1
while [ "$sample" -le "$SAMPLES" ]; do
  metrics="$(profile_curl /tmp/chat-body "$MICROAGENT_MODEL_URL/chat/completions" -H "Content-Type: application/json" -d "$chat_payload")"
  set -- $metrics
  status="${1:-000}"
  printf 'PROFILE\tchat\t%s\t%s\t%s\t%s\t%s\t\n' "$sample" "$status" "${2:-0}" "${3:-0}" "${4:-0}"
  test "$status" = "$EXPECTED_STATUS"
  if [ "$EXPECTED_STATUS" = "200" ]; then
    grep -q '"choices"' /tmp/chat-body
  fi
  sample=$((sample + 1))
done

sample=1
while [ "$sample" -le "$SAMPLES" ]; do
  metrics="$(profile_curl /tmp/stream-body -N "$MICROAGENT_MODEL_URL/chat/completions" -H "Content-Type: application/json" -d "$stream_payload")"
  set -- $metrics
  status="${1:-000}"
  chunks="$(grep -c '^data:' /tmp/stream-body || true)"
  printf 'PROFILE\tstream\t%s\t%s\t%s\t%s\t%s\t%s\n' "$sample" "$status" "${2:-0}" "${3:-0}" "${4:-0}" "$chunks"
  test "$status" = "$EXPECTED_STATUS"
  if [ "$EXPECTED_STATUS" = "200" ]; then
    grep -q '^data:' /tmp/stream-body
  fi
  sample=$((sample + 1))
done
EOF
)"

  echo "microagent-e2e-model-mediation-llamacpp: case=$label mode=$mode expected_http=$expected_status"
  set_telemetry_phase "$label"
  if ! env "${env_args[@]}" "$CLI" run --name "$workspace" \
    --env "EXPECTED_STATUS=$expected_status" \
    --env "REQUEST_MODEL=$REQUEST_MODEL" \
    --env "CHAT_TOKENS=$CHAT_TOKENS" \
    --env "STREAM_TOKENS=$STREAM_TOKENS" \
    --env "SAMPLES=$SAMPLES" \
    "${RUN_FLAGS[@]}" sh -c "$guest_script" >"$run_log" 2>&1; then
    cat "$run_log" >&2
    fail "case $label failed"
  fi
  extract_guest_stdout "$run_log" "$stdout_path"
  grep -q $'PROFILE\tmodels' "$stdout_path" || fail "case $label missing model profile rows"
  grep -q $'PROFILE\tchat' "$stdout_path" || fail "case $label missing chat profile rows"
  grep -q $'PROFILE\tstream' "$stdout_path" || fail "case $label missing stream profile rows"
  capture_audit_log "$workspace"
  if [ "$mode" != "off" ]; then
    assert_audit_contains "$workspace" "request_end"
  fi
  assert_index_clean
  assert_single_runner_reused
  summarize_audit "$label" "$workspace" "$expected_status"
  summarize_profiles "$label" "$expected_status" "$stdout_path"
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
start_telemetry

run_case "direct" "off" "200"
run_case "local" "local-allow" "200"
assert_audit_contains "mml-local" "mediation_decision_allow"
assert_audit_contains "mml-local" "upstream_headers"

start_policy allow "allow"
run_case "pa" "policy" "200"
assert_audit_contains "mml-pa" "mediation_decision_allow"
assert_audit_contains "mml-pa" "upstream_headers"
stop_policy

start_policy deny "deny"
run_case "pd" "policy" "403"
assert_audit_contains "mml-pd" "mediation_decision_deny"
assert_audit_lacks "mml-pd" "upstream_headers"
stop_policy

unavailable_port="$(choose_port)"
POLICY_URL="http://127.0.0.1:${unavailable_port}/decision"
run_case "pu" "policy" "503"
assert_audit_contains "mml-pu" "mediation_decision_error"
assert_audit_lacks "mml-pu" "upstream_headers"
POLICY_URL=""

echo "microagent-e2e-model-mediation-llamacpp: summary"
cat "$OUT_DIR/summary.tsv"
write_profile_summaries
echo "microagent-e2e-model-mediation-llamacpp: profile summary"
cat "$OUT_DIR/profile-summary.tsv"
echo "microagent-e2e-model-mediation-llamacpp: direct-vs-mediated profile comparison"
cat "$OUT_DIR/profile-comparison.tsv"
write_telemetry_summary
if [ -s "$OUT_DIR/telemetry-summary.tsv" ]; then
  echo "microagent-e2e-model-mediation-llamacpp: telemetry summary"
  cat "$OUT_DIR/telemetry-summary.tsv"
fi
write_gate_summary
echo "microagent-e2e-model-mediation-llamacpp: mediation gates"
cat "$OUT_DIR/mediation-gates.tsv"
echo "PASS microagent-e2e-model-mediation-llamacpp: production model mediation matrix passed with llama.cpp"
