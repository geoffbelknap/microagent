#!/usr/bin/env bash
#
# microagent-e2e-model-mediation-pressure.sh - runner-neutral pressure probe
# for the experimental production model mediator.
#
# This script expects an OpenAI-compatible host runner that can be started
# through `microagent model serve` or handed in by a backend adapter. It compares
# direct bridge traffic with local mediation, file-policy mediation, and
# external-policy mediation under configurable guest concurrency.
#
# Required:
#   MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE=1
#   MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_MODEL_REF
#
# Optional:
#   MICROAGENT_CLI
#   MICROAGENT_FIRECRACKER
#   MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_OUT_DIR
#   MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_STATE_DIR
#   MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_IMAGE
#   MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_LABEL
#   MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_CASE_PREFIX
#   MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_ENV_FILE
#   MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_PID
#   MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_PORT
#   MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_ENGINE
#   MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_REQUEST_MODEL
#   MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_OWN_OUT_DIR
#   MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_WORKSPACES
#   MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_CONCURRENCY
#   MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_CASES
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

CLI="${MICROAGENT_CLI:-$(e2e_exe "$ROOT/.build/dev/microagent")}"
LABEL="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_LABEL:-runner}"
CASE_PREFIX="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_CASE_PREFIX:-mmp}"
OUT_DIR="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_OUT_DIR:-/tmp/ma-e2e-mm-pressure-$(date +%Y%m%d%H%M%S)}"
STATE_DIR="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_STATE_DIR:-$OUT_DIR/state}"
IMAGE="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_IMAGE:-quay.io/curl/curl:latest}"
MODEL_REF="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_MODEL_REF:-}"
RUNNER_ENV_FILE="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_ENV_FILE:-}"
RUNNER_PID="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_PID:-}"
RUNNER_PORT="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_PORT:-}"
RUNNER_ENGINE="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_ENGINE:-}"
REQUEST_MODEL="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_REQUEST_MODEL:-}"
OWN_OUT_DIR="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_OWN_OUT_DIR:-1}"
KEEP_FAILED="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_KEEP:-${MICROAGENT_KEEP_MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE:-0}}"
KEEP_STATE="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_KEEP_STATE:-0}"
WORKSPACES="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_WORKSPACES:-2}"
CONCURRENCY="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_CONCURRENCY:-1,2}"
CASES="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_CASES:-direct,local,pf,pa}"
SAMPLES="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_SAMPLES:-2}"
WARMUPS="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_WARMUPS:-1}"
CHAT_TOKENS="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_CHAT_TOKENS:-64}"
STREAM_TOKENS="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_STREAM_TOKENS:-96}"
TELEMETRY="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_TELEMETRY:-auto}"
TELEMETRY_INTERVAL="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_TELEMETRY_INTERVAL:-0.5}"
TELEMETRY_ENDPOINTS="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_TELEMETRY_ENDPOINTS:-/metrics,/health}"
GATE_MODE="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_GATE_MODE:-warn}"
MAX_MODELS_TOTAL_P95_DELTA_MS="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_MAX_MODELS_TOTAL_P95_DELTA_MS:-250}"
MAX_CHAT_TOTAL_P95_DELTA_MS="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_MAX_CHAT_TOTAL_P95_DELTA_MS:-1000}"
MAX_STREAM_TTFB_P95_DELTA_MS="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_MAX_STREAM_TTFB_P95_DELTA_MS:-500}"
MAX_DECISION_P95_MS="${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_MAX_DECISION_P95_MS:-250}"
POLICY_PID=""
POLICY_URL=""
POLICY_FILE=""
STARTED_RUNNER=0
TELEMETRY_PID=""
TELEMETRY_PHASE_FILE=""
RUN_FLAGS=()
WORKSPACE_NAMES=()

skip() { e2e_skip "microagent-e2e-model-mediation-pressure: $1"; }
fail() { echo "FAIL microagent-e2e-model-mediation-pressure: $1" >&2; exit 1; }

usage() {
  cat <<'EOF'
microagent-e2e-model-mediation-pressure.sh

Runner-neutral pressure probe for the experimental production model mediator.

Adapter handoff example:
  MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_MODEL_REF=org/repo/model.gguf \
  MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_PID=1234 \
  MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_PORT=8080 \
  scripts/dev/microagent-e2e-model-mediation-pressure.sh

Useful knobs:
  MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_WORKSPACES=2
  MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_CONCURRENCY=1,2
  MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_CASES=direct,local,pf,pa
  MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_SAMPLES=2
  MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_WARMUPS=1

Cases:
  direct  mediation off
  local   local allow mediation
  pf      structured file policy allow
  pa      external policy URL allow
EOF
}

case "${1:-}" in
  -h|--help|help)
    usage
    exit 0
    ;;
esac

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

stop_policy() {
  if [ -n "$POLICY_PID" ]; then
    kill "$POLICY_PID" >/dev/null 2>&1 || true
    wait "$POLICY_PID" >/dev/null 2>&1 || true
    POLICY_PID=""
  fi
  POLICY_URL=""
}

cleanup() {
  local status=$?
  local workspace
  set +e
  stop_policy
  stop_telemetry
  if [ "$KEEP_STATE" = "1" ] && [ "$status" -ne 0 ]; then
    echo "microagent-e2e-model-mediation-pressure: preserved workspace state under $STATE_DIR" >&2
  else
    for workspace in "${WORKSPACE_NAMES[@]}"; do
      "$CLI" kill "$workspace" --backend firecracker --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
      "$CLI" delete "$workspace" --force --yes --backend firecracker --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    done
  fi
  if [ "$STARTED_RUNNER" = "1" ] && [ -n "$MODEL_REF" ]; then
    "$CLI" model stop "$MODEL_REF" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  fi
  if [ "$OWN_OUT_DIR" != "1" ]; then
    exit "$status"
  fi
  if [ "$KEEP_FAILED" = "1" ]; then
    if [ "$status" -ne 0 ]; then
      echo "microagent-e2e-model-mediation-pressure: preserved failed state under $OUT_DIR" >&2
    else
      echo "microagent-e2e-model-mediation-pressure: preserved reports under $OUT_DIR" >&2
    fi
  else
    rm -rf "$OUT_DIR"
  fi
  exit "$status"
}
trap cleanup EXIT

runner_env_args() {
  if [ -n "$RUNNER_ENV_FILE" ]; then
    [ -r "$RUNNER_ENV_FILE" ] || fail "runner env file is not readable: $RUNNER_ENV_FILE"
    sed '/^[[:space:]]*$/d' "$RUNNER_ENV_FILE"
  fi
}

normalize_csv_spaces() {
  printf '%s\n' "$1" | tr ',' ' ' | xargs
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
      fail "MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_TELEMETRY must be off, auto, or required"
      ;;
  esac
  [ -n "$RUNNER_PORT" ] || fail "runner port is required for telemetry"
  TELEMETRY_PHASE_FILE="$OUT_DIR/pressure-telemetry.phase"
  printf '%s\n' startup >"$TELEMETRY_PHASE_FILE"
  python3 "$ROOT/scripts/dev/microagent-model-mediation-telemetry.py" sample \
    --runner-root-url "http://127.0.0.1:$RUNNER_PORT" \
    --phase-file "$TELEMETRY_PHASE_FILE" \
    --runner-out "$OUT_DIR/pressure-runner-telemetry.jsonl" \
    --gpu-out "$OUT_DIR/pressure-gpu-telemetry.csv" \
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
  echo "microagent-e2e-model-mediation-pressure: telemetry writing to $OUT_DIR/pressure-runner-telemetry.jsonl and $OUT_DIR/pressure-gpu-telemetry.csv"
}

write_telemetry_summary() {
  if [ "$TELEMETRY" = "off" ]; then
    return
  fi
  stop_telemetry
  python3 "$ROOT/scripts/dev/microagent-model-mediation-telemetry.py" summary \
    --runner-in "$OUT_DIR/pressure-runner-telemetry.jsonl" \
    --gpu-in "$OUT_DIR/pressure-gpu-telemetry.csv" \
    --adapter "$LABEL" \
    --out "$OUT_DIR/pressure-telemetry-summary.tsv"
}

discover_request_model() {
  [ -n "$RUNNER_PORT" ] || fail "runner port is required to discover the served model"
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
  STARTED_RUNNER=1
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
  if [ -z "$RUNNER_ENGINE" ]; then
    RUNNER_ENGINE="$(python3 - "$runner_json" <<'PY'
import json
import sys
from pathlib import Path

doc = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
print(doc.get("engine") or "")
PY
)"
  fi
  if [ -z "$REQUEST_MODEL" ]; then
    REQUEST_MODEL="$(discover_request_model)"
  fi
  echo "microagent-e2e-model-mediation-pressure: pinned $LABEL runner pid=$RUNNER_PID model=$REQUEST_MODEL"
}

assert_single_runner_reused() {
  local runners_json="$OUT_DIR/pressure-runners.json"
  [ -n "$RUNNER_PID" ] || fail "runner pid is required"
  "$CLI" --json model runners --state-dir "$STATE_DIR" >"$runners_json"
  python3 - "$runners_json" "$RUNNER_PID" "$RUNNER_ENGINE" <<'PY'
import json
import sys
from pathlib import Path

doc = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
want_pid = int(sys.argv[2])
want_engine = sys.argv[3]
runners = doc.get("runners") or []
if len(runners) != 1:
    raise SystemExit(f"runner count = {len(runners)}, want 1")
runner = runners[0]
if runner.get("pid") != want_pid:
    raise SystemExit(f"runner pid = {runner.get('pid')}, want {want_pid}")
if want_engine and runner.get("engine") != want_engine:
    raise SystemExit(f"runner engine = {runner.get('engine')!r}, want {want_engine!r}")
PY
}

write_file_policy() {
  local path="$1"
  cat >"$path" <<EOF
{
  "schema_version": "microagent.model_policy.v1",
  "default": "deny",
  "rules": [
    {
      "id": "models",
      "effect": "allow",
      "match": {
        "methods": ["GET"],
        "paths": ["/v1/models"]
      }
    },
    {
      "id": "chat",
      "effect": "allow",
      "match": {
        "methods": ["POST"],
        "paths": ["/v1/chat/completions"],
        "models": ["$REQUEST_MODEL"]
      },
      "limits": {
        "max_request_bytes": 65536,
        "max_text_bytes": 4096,
        "max_messages": 4,
        "max_tokens": 4096
      }
    }
  ]
}
EOF
}

validate_file_policy() {
  local path="$1"
  "$CLI" --json model policy validate "$path" >"$OUT_DIR/pressure-policy-validate.json"
  "$CLI" --json model policy evaluate "$path" \
    --method POST \
    --path /v1/chat/completions \
    --model "$REQUEST_MODEL" \
    --max-tokens "$CHAT_TOKENS" \
    --stream false \
    --text-bytes 24 \
    --messages 1 \
    --expect allow >"$OUT_DIR/pressure-policy-chat-evaluate.json"
  "$CLI" --json model policy evaluate "$path" \
    --method POST \
    --path /v1/chat/completions \
    --model "$REQUEST_MODEL" \
    --max-tokens 4097 \
    --stream false \
    --text-bytes 24 \
    --messages 1 \
    --expect deny >"$OUT_DIR/pressure-policy-chat-over-max-tokens-evaluate.json"
}

audit_log_for_workspace() {
  local workspace="$1"
  printf '%s\n' "$STATE_DIR/host-workers/${workspace}_model.openai.jsonl"
}

audit_report_for_workspace() {
  local workspace="$1"
  printf '%s\n' "$OUT_DIR/${workspace}_model.openai.jsonl"
}

capture_audit_log() {
  local case_name="$1"
  local level="$2"
  local workspace="$3"
  local live_log
  local report_log
  live_log="$(audit_log_for_workspace "$workspace")"
  report_log="$(audit_report_for_workspace "$workspace")"
  if [ -e "$live_log" ]; then
    cp "$live_log" "$report_log"
    printf '%s\t%s\t%s\t%s\n' "$case_name" "$level" "$workspace" "$report_log" >>"$OUT_DIR/pressure-audit-index.tsv"
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

assert_audit_contains() {
  local workspace="$1"
  local needle="$2"
  local log_path
  log_path="$(audit_report_for_workspace "$workspace")"
  [ -r "$log_path" ] || fail "audit log not readable for $workspace: $log_path"
  grep -q "\"event\":\"$needle\"" "$log_path" || fail "audit log $log_path missing event $needle"
}

assert_audit_no_prompt_body() {
  local workspace="$1"
  local log_path
  log_path="$(audit_report_for_workspace "$workspace")"
  [ -r "$log_path" ] || fail "audit log not readable for $workspace: $log_path"
  if grep -Fq "Reply with exactly PONG." "$log_path" || grep -Fq "mediated host model workers" "$log_path"; then
    fail "audit log $log_path leaked prompt body text"
  fi
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

append_profiles() {
  local stdout_path="$1"
  awk 'BEGIN {FS=OFS="\t"} /^PROFILE\t/ {matched=1; $1=""; sub(/^\t/, ""); print} END {exit matched ? 0 : 1}' "$stdout_path" >>"$OUT_DIR/pressure-profiles.tsv" || fail "missing pressure profile rows in $stdout_path"
}

write_pressure_summaries() {
  python3 - "$OUT_DIR/pressure-profiles.tsv" "$OUT_DIR/pressure-profile-summary.tsv" "$OUT_DIR/pressure-profile-comparison.tsv" "$OUT_DIR/pressure-audit-index.tsv" "$OUT_DIR/pressure-audit-summary.tsv" "$OUT_DIR/pressure-gates.tsv" "$GATE_MODE" "$MAX_MODELS_TOTAL_P95_DELTA_MS" "$MAX_CHAT_TOTAL_P95_DELTA_MS" "$MAX_STREAM_TTFB_P95_DELTA_MS" "$MAX_DECISION_P95_MS" <<'PY'
import csv
import json
import math
import statistics
import sys
from collections import Counter, defaultdict
from pathlib import Path

profiles_path = Path(sys.argv[1])
summary_path = Path(sys.argv[2])
comparison_path = Path(sys.argv[3])
audit_index_path = Path(sys.argv[4])
audit_summary_path = Path(sys.argv[5])
gates_path = Path(sys.argv[6])
gate_mode = sys.argv[7]
limits = {
    "models_total_p95_delta_ms": float(sys.argv[8]),
    "chat_total_p95_delta_ms": float(sys.argv[9]),
    "stream_ttfb_p95_delta_ms": float(sys.argv[10]),
    "decision_p95_ms": float(sys.argv[11]),
}

def as_float(raw):
    if raw in (None, ""):
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

def ms(raw):
    value = as_float(raw)
    return None if value is None else value * 1000

rows = list(csv.DictReader(profiles_path.open(encoding="utf-8"), delimiter="\t"))
grouped = defaultdict(list)
for row in rows:
    grouped[(row["case"], row["level"], row["endpoint"])].append(row)

summary = {}
for key, group in grouped.items():
    totals = [value for row in group if (value := ms(row.get("total_s"))) is not None]
    ttfbs = [value for row in group if (value := ms(row.get("ttfb_s"))) is not None]
    statuses = sorted({row.get("status", "") for row in group})
    summary[key] = {
        "samples": str(len(group)),
        "status": ",".join(statuses),
        "total_p50_ms": statistics.median(totals) if totals else None,
        "total_p95_ms": percentile(totals, 95),
        "ttfb_p50_ms": statistics.median(ttfbs) if ttfbs else None,
        "ttfb_p95_ms": percentile(ttfbs, 95),
    }

with summary_path.open("w", encoding="utf-8", newline="") as handle:
    writer = csv.writer(handle, delimiter="\t")
    writer.writerow(["case", "level", "endpoint", "status", "samples", "total_p50_ms", "total_p95_ms", "ttfb_p50_ms", "ttfb_p95_ms"])
    for key in sorted(summary, key=lambda item: (int(item[1]), item[0], item[2])):
        data = summary[key]
        writer.writerow([key[0], key[1], key[2], data["status"], data["samples"], fmt(data["total_p50_ms"]), fmt(data["total_p95_ms"]), fmt(data["ttfb_p50_ms"]), fmt(data["ttfb_p95_ms"])])

comparison_rows = []
for (case, level, endpoint), data in summary.items():
    if case == "direct":
        continue
    direct = summary.get(("direct", level, endpoint))
    if not direct:
        continue
    direct_total_p95 = direct["total_p95_ms"]
    case_total_p95 = data["total_p95_ms"]
    direct_ttfb_p95 = direct["ttfb_p95_ms"]
    case_ttfb_p95 = data["ttfb_p95_ms"]
    comparison_rows.append(
        {
            "level": level,
            "endpoint": endpoint,
            "case": case,
            "status": data["status"],
            "direct_total_p95_ms": fmt(direct_total_p95),
            "case_total_p95_ms": fmt(case_total_p95),
            "delta_total_p95_ms": fmt(case_total_p95 - direct_total_p95) if direct_total_p95 is not None and case_total_p95 is not None else "",
            "direct_ttfb_p95_ms": fmt(direct_ttfb_p95),
            "case_ttfb_p95_ms": fmt(case_ttfb_p95),
            "delta_ttfb_p95_ms": fmt(case_ttfb_p95 - direct_ttfb_p95) if direct_ttfb_p95 is not None and case_ttfb_p95 is not None else "",
        }
    )

with comparison_path.open("w", encoding="utf-8", newline="") as handle:
    fields = ["level", "endpoint", "case", "status", "direct_total_p95_ms", "case_total_p95_ms", "delta_total_p95_ms", "direct_ttfb_p95_ms", "case_ttfb_p95_ms", "delta_ttfb_p95_ms"]
    writer = csv.DictWriter(handle, fieldnames=fields, delimiter="\t")
    writer.writeheader()
    writer.writerows(sorted(comparison_rows, key=lambda row: (int(row["level"]), row["endpoint"], row["case"])))

audit_groups = defaultdict(list)
if audit_index_path.exists():
    with audit_index_path.open(encoding="utf-8") as handle:
        for raw in handle:
            if not raw.strip():
                continue
            case, level, workspace, log = raw.rstrip("\n").split("\t")
            audit_groups[(case, level)].append((workspace, Path(log)))

audit_summary = {}
with audit_summary_path.open("w", encoding="utf-8", newline="") as handle:
    writer = csv.writer(handle, delimiter="\t")
    writer.writerow(["case", "level", "workspaces", "audit_events", "mediation_results", "request_end_p95_ms", "decision_p95_ms", "logs"])
    for key in sorted(audit_groups, key=lambda item: (int(item[1]), item[0])):
        events = []
        logs = []
        for _workspace, log in audit_groups[key]:
            logs.append(str(log))
            if not log.exists():
                continue
            for line in log.read_text(encoding="utf-8").splitlines():
                if line.strip():
                    events.append(json.loads(line))
        counter = Counter(str(event.get("event")) for event in events)
        results = Counter(str(event.get("mediation_result")) for event in events if event.get("mediation_result") is not None)
        durations = [float(event.get("duration_ms")) for event in events if event.get("event") == "request_end" and event.get("duration_ms") is not None]
        decisions = [float(event.get("mediation_decision_ms")) for event in events if event.get("mediation_decision_ms") is not None]
        request_p95 = percentile(durations, 95)
        decision_p95 = percentile(decisions, 95)
        audit_summary[key] = {"request_end_p95_ms": request_p95, "decision_p95_ms": decision_p95}
        writer.writerow([
            key[0],
            key[1],
            len(audit_groups[key]),
            ",".join(f"{k}:{v}" for k, v in sorted(counter.items())),
            ",".join(f"{k}:{v}" for k, v in sorted(results.items())),
            fmt(request_p95),
            fmt(decision_p95),
            ",".join(logs),
        ])

gate_rows = []

def add_gate(level, case, name, actual, limit, detail):
    if actual is not None and actual < 0:
        actual = 0.0
    status = "missing" if actual is None else ("pass" if actual <= limit else "fail")
    gate_rows.append({
        "level": level,
        "case": case,
        "gate": name,
        "actual": fmt(actual),
        "limit": fmt(limit),
        "status": status,
        "detail": detail,
    })

for row in comparison_rows:
    endpoint = row["endpoint"]
    case = row["case"]
    level = row["level"]
    if endpoint == "models":
        add_gate(level, case, "models_total_p95_delta", as_float(row["delta_total_p95_ms"]), limits["models_total_p95_delta_ms"], "guest pressure models p95 delta")
    elif endpoint == "chat":
        add_gate(level, case, "chat_total_p95_delta", as_float(row["delta_total_p95_ms"]), limits["chat_total_p95_delta_ms"], "guest pressure chat p95 delta")
    elif endpoint == "stream":
        add_gate(level, case, "stream_ttfb_p95_delta", as_float(row["delta_ttfb_p95_ms"]), limits["stream_ttfb_p95_delta_ms"], "guest pressure stream ttfb p95 delta")

for (case, level), data in audit_summary.items():
    if case != "direct":
        add_gate(level, case, "decision_p95", data["decision_p95_ms"], limits["decision_p95_ms"], "mediator decision p95")

with gates_path.open("w", encoding="utf-8", newline="") as handle:
    fields = ["level", "case", "gate", "actual", "limit", "status", "detail"]
    writer = csv.DictWriter(handle, fieldnames=fields, delimiter="\t")
    writer.writeheader()
    writer.writerows(sorted(gate_rows, key=lambda row: (int(row["level"]), row["case"], row["gate"])))

failures = [row for row in gate_rows if row["status"] != "pass"]
if failures and gate_mode == "required":
    for row in failures:
        print(f"{row['case']} c={row['level']} {row['gate']} {row['status']}: actual={row['actual']} limit={row['limit']} {row['detail']}", file=sys.stderr)
    raise SystemExit(1)
PY
}

case_mode() {
  case "$1" in
    direct) printf '%s\n' off ;;
    local) printf '%s\n' local-allow ;;
    pf|pa) printf '%s\n' policy ;;
    *) fail "unknown pressure case: $1" ;;
  esac
}

run_pressure_case() {
  local case_name="$1"
  local mode
  local level
  local workspace
  local idx
  local run_log
  local stdout_path
  local status
  local pid
  local -a pids
  mapfile -t runner_env < <(runner_env_args)
  mode="$(case_mode "$case_name")"

  if [ "$case_name" = "pf" ]; then
    POLICY_FILE="$OUT_DIR/pressure-policy-file-allow.json"
    write_file_policy "$POLICY_FILE"
    validate_file_policy "$POLICY_FILE"
  elif [ "$case_name" = "pa" ]; then
    start_policy allow "pressure-allow"
  else
    POLICY_FILE=""
    stop_policy
  fi

  for level in $(normalize_csv_spaces "$CONCURRENCY"); do
    set_telemetry_phase "$case_name:c=$level"
    echo "microagent-e2e-model-mediation-pressure: adapter=$LABEL case=$case_name mode=$mode workspaces=$WORKSPACES per_workspace_concurrency=$level"
    pids=()
    idx=1
    while [ "$idx" -le "$WORKSPACES" ]; do
      workspace="$CASE_PREFIX-$case_name-c$level-w$idx"
      WORKSPACE_NAMES+=("$workspace")
      run_log="$OUT_DIR/$workspace.run.log"
      env_args=(
        "${runner_env[@]}"
        "MICROAGENT_MODEL_MEDIATION=$mode"
        "MICROAGENT_MODEL_POLICY_TIMEOUT=1s"
      )
      if [ -n "$POLICY_URL" ]; then
        env_args+=("MICROAGENT_MODEL_POLICY_URL=$POLICY_URL")
      else
        env_args+=("MICROAGENT_MODEL_POLICY_URL=")
      fi
      if [ -n "$POLICY_FILE" ]; then
        env_args+=("MICROAGENT_MODEL_POLICY_FILE=$POLICY_FILE")
      else
        env_args+=("MICROAGENT_MODEL_POLICY_FILE=")
      fi
      env "${env_args[@]}" "$CLI" run --name "$workspace" \
        --env "PRESSURE_CASE=$case_name" \
        --env "PRESSURE_LEVEL=$level" \
        --env "PRESSURE_WORKSPACE=$workspace" \
        --env "PRESSURE_CONCURRENCY=$level" \
        --env "PRESSURE_SAMPLES=$SAMPLES" \
        --env "PRESSURE_WARMUPS=$WARMUPS" \
        --env "PRESSURE_REQUEST_MODEL=$REQUEST_MODEL" \
        --env "PRESSURE_CHAT_TOKENS=$CHAT_TOKENS" \
        --env "PRESSURE_STREAM_TOKENS=$STREAM_TOKENS" \
        "${RUN_FLAGS[@]}" sh -c "$GUEST_PRESSURE_SCRIPT" >"$run_log" 2>&1 &
      pids+=("$!")
      idx=$((idx + 1))
    done

    status=0
    for pid in "${pids[@]}"; do
      wait "$pid" || status=1
    done
    if [ "$status" -ne 0 ]; then
      for run_log in "$OUT_DIR"/"$CASE_PREFIX-$case_name-c$level"-w*.run.log; do
        [ ! -s "$run_log" ] || cat "$run_log" >&2
      done
      fail "pressure case $case_name c=$level failed"
    fi

    idx=1
    while [ "$idx" -le "$WORKSPACES" ]; do
      workspace="$CASE_PREFIX-$case_name-c$level-w$idx"
      run_log="$OUT_DIR/$workspace.run.log"
      stdout_path="$OUT_DIR/$workspace.guest.stdout"
      extract_guest_stdout "$run_log" "$stdout_path"
      append_profiles "$stdout_path"
      capture_audit_log "$case_name" "$level" "$workspace"
      if [ "$mode" != "off" ]; then
        assert_audit_contains "$workspace" "request_end"
        assert_audit_contains "$workspace" "upstream_headers"
        assert_audit_no_prompt_body "$workspace"
      fi
      idx=$((idx + 1))
    done
    assert_index_clean
    assert_single_runner_reused
  done

  POLICY_FILE=""
  stop_policy
}

case "${MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE:-0}" in
  1|true|TRUE|yes|YES|required)
    ;;
  *)
    skip "set MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE=1 to run the opt-in runner-neutral pressure scenario"
    ;;
esac
case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    skip "runner-neutral model mediation pressure E2E currently targets the Linux host backend"
    ;;
esac
[ -n "$MODEL_REF" ] || fail "MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_MODEL_REF is required"
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
for numeric in WORKSPACES SAMPLES WARMUPS CHAT_TOKENS STREAM_TOKENS; do
  value="${!numeric}"
  case "$value" in
    ''|*[!0-9]*) fail "$numeric must be a non-negative integer" ;;
  esac
done
if [ "$WORKSPACES" -le 0 ] || [ "$SAMPLES" -le 0 ] || [ "$CHAT_TOKENS" -le 0 ] || [ "$STREAM_TOKENS" -le 0 ]; then
  fail "WORKSPACES, SAMPLES, CHAT_TOKENS, and STREAM_TOKENS must be > 0"
fi
case "$TELEMETRY" in
  off|auto|required) ;;
  *) fail "MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_TELEMETRY must be off, auto, or required" ;;
esac
case "$GATE_MODE" in
  off|warn|required) ;;
  *) fail "MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_GATE_MODE must be off, warn, or required" ;;
esac
case "$KEEP_STATE" in
  1|true|TRUE|yes|YES)
    KEEP_STATE=1
    ;;
  *)
    KEEP_STATE=0
    ;;
esac
for level in $(normalize_csv_spaces "$CONCURRENCY"); do
  case "$level" in
    ''|*[!0-9]*) fail "MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_CONCURRENCY must be positive integer levels" ;;
  esac
  if [ "$level" -le 0 ]; then
    fail "MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_CONCURRENCY levels must be > 0"
  fi
done
for case_name in $(normalize_csv_spaces "$CASES"); do
  case "$case_name" in
    direct|local|pf|pa) ;;
    *) fail "MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_CASES contains unknown case: $case_name" ;;
  esac
done

mkdir -p "$OUT_DIR" "$STATE_DIR"
printf 'case\tlevel\tworkspace\tendpoint\tsample\tstatus\tttfb_s\ttotal_s\tbytes\tchunks\n' >"$OUT_DIR/pressure-profiles.tsv"
: >"$OUT_DIR/pressure-audit-index.tsv"

RUN_FLAGS=(--backend firecracker --network isolated --state-dir "$STATE_DIR" --model "$MODEL_REF")
if [ "$KEEP_STATE" = "0" ]; then
  RUN_FLAGS+=(--rm)
fi
RUN_FLAGS+=("$IMAGE")

if [ -z "$RUNNER_PID" ] || [ -z "$RUNNER_PORT" ]; then
  start_pinned_runner
fi
if [ -z "$REQUEST_MODEL" ]; then
  REQUEST_MODEL="$(discover_request_model)"
fi

assert_single_runner_reused
start_telemetry

read -r -d '' GUEST_PRESSURE_SCRIPT <<'SH' || true
set -eu

: "${MICROAGENT_MODEL_URL:?}"
: "${PRESSURE_CASE:?}"
: "${PRESSURE_LEVEL:?}"
: "${PRESSURE_WORKSPACE:?}"
: "${PRESSURE_CONCURRENCY:?}"
: "${PRESSURE_SAMPLES:?}"
: "${PRESSURE_WARMUPS:?}"
: "${PRESSURE_REQUEST_MODEL:?}"
: "${PRESSURE_CHAT_TOKENS:?}"
: "${PRESSURE_STREAM_TOKENS:?}"

profile_curl() {
  out="$1"
  shift
  attempt=1
  while :; do
    metrics="$(curl -sS -o "$out" -w "%{http_code} %{time_starttransfer} %{time_total} %{size_download}" "$@" 2>/tmp/pressure-curl.err || true)"
    status="${metrics%% *}"
    status="${status:-000}"
    if [ "$status" != "000" ] || [ "$attempt" -ge 50 ]; then
      if [ "$status" = "000" ] && [ -s /tmp/pressure-curl.err ]; then
        cat /tmp/pressure-curl.err >&2
      fi
      printf '%s\n' "$metrics"
      return
    fi
    attempt=$((attempt + 1))
    sleep 0.1
  done
}

emit_profile() {
  endpoint="$1"
  sample="$2"
  status="$3"
  ttfb="$4"
  total="$5"
  bytes="$6"
  chunks="${7:-}"
  printf 'PROFILE\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$PRESSURE_CASE" "$PRESSURE_LEVEL" "$PRESSURE_WORKSPACE" "$endpoint" "$sample" "$status" "$ttfb" "$total" "$bytes" "$chunks"
}

measure_models_worker() {
  worker="$1"
  out="$2"
  i=0
  total=$((PRESSURE_WARMUPS + PRESSURE_SAMPLES))
  while [ "$i" -lt "$total" ]; do
    body="$(mktemp)"
    metrics="$(profile_curl "$body" "$MICROAGENT_MODEL_URL/models")"
    set -- $metrics
    status="${1:-000}"
    test "$status" = "200"
    grep -q "$PRESSURE_REQUEST_MODEL" "$body"
    if [ "$i" -ge "$PRESSURE_WARMUPS" ]; then
      sample="$worker.$((i - PRESSURE_WARMUPS + 1))"
      emit_profile models "$sample" "$status" "${2:-0}" "${3:-0}" "${4:-0}" >>"$out"
    fi
    rm -f "$body"
    i=$((i + 1))
  done
}

measure_chat_worker() {
  worker="$1"
  out="$2"
  payload='{"model":"'"$PRESSURE_REQUEST_MODEL"'","messages":[{"role":"user","content":"Reply with exactly PONG."}],"max_tokens":'"$PRESSURE_CHAT_TOKENS"',"temperature":0,"stream":false}'
  i=0
  total=$((PRESSURE_WARMUPS + PRESSURE_SAMPLES))
  while [ "$i" -lt "$total" ]; do
    body="$(mktemp)"
    metrics="$(profile_curl "$body" "$MICROAGENT_MODEL_URL/chat/completions" -H "Content-Type: application/json" -d "$payload")"
    set -- $metrics
    status="${1:-000}"
    test "$status" = "200"
    grep -q '"choices"' "$body"
    if [ "$i" -ge "$PRESSURE_WARMUPS" ]; then
      sample="$worker.$((i - PRESSURE_WARMUPS + 1))"
      emit_profile chat "$sample" "$status" "${2:-0}" "${3:-0}" "${4:-0}" >>"$out"
    fi
    rm -f "$body"
    i=$((i + 1))
  done
}

measure_stream_worker() {
  worker="$1"
  out="$2"
  payload='{"model":"'"$PRESSURE_REQUEST_MODEL"'","messages":[{"role":"user","content":"Write one compact sentence about mediated host model workers."}],"max_tokens":'"$PRESSURE_STREAM_TOKENS"',"temperature":0,"stream":true}'
  i=0
  total=$((PRESSURE_WARMUPS + PRESSURE_SAMPLES))
  while [ "$i" -lt "$total" ]; do
    body="$(mktemp)"
    metrics="$(profile_curl "$body" -N "$MICROAGENT_MODEL_URL/chat/completions" -H "Content-Type: application/json" -d "$payload")"
    set -- $metrics
    status="${1:-000}"
    test "$status" = "200"
    grep -q '^data:' "$body"
    chunks="$(grep -c '^data:' "$body" || true)"
    if [ "$i" -ge "$PRESSURE_WARMUPS" ]; then
      sample="$worker.$((i - PRESSURE_WARMUPS + 1))"
      emit_profile stream "$sample" "$status" "${2:-0}" "${3:-0}" "${4:-0}" "$chunks" >>"$out"
    fi
    rm -f "$body"
    i=$((i + 1))
  done
}

run_endpoint() {
  endpoint="$1"
  tmp="$(mktemp -d)"
  pids=""
  worker=1
  while [ "$worker" -le "$PRESSURE_CONCURRENCY" ]; do
    case "$endpoint" in
      models) measure_models_worker "$worker" "$tmp/$worker.out" & ;;
      chat) measure_chat_worker "$worker" "$tmp/$worker.out" & ;;
      stream) measure_stream_worker "$worker" "$tmp/$worker.out" & ;;
    esac
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
  worker=1
  while [ "$worker" -le "$PRESSURE_CONCURRENCY" ]; do
    cat "$tmp/$worker.out"
    worker=$((worker + 1))
  done
  rm -rf "$tmp"
}

run_endpoint models
run_endpoint chat
run_endpoint stream
SH

for case_name in $(normalize_csv_spaces "$CASES"); do
  run_pressure_case "$case_name"
done

write_pressure_summaries

echo "microagent-e2e-model-mediation-pressure: profile summary"
cat "$OUT_DIR/pressure-profile-summary.tsv"
echo "microagent-e2e-model-mediation-pressure: direct-vs-mediated pressure comparison"
cat "$OUT_DIR/pressure-profile-comparison.tsv"
echo "microagent-e2e-model-mediation-pressure: audit summary"
cat "$OUT_DIR/pressure-audit-summary.tsv"
write_telemetry_summary
if [ -s "$OUT_DIR/pressure-telemetry-summary.tsv" ]; then
  echo "microagent-e2e-model-mediation-pressure: telemetry summary"
  cat "$OUT_DIR/pressure-telemetry-summary.tsv"
fi
echo "microagent-e2e-model-mediation-pressure: pressure gates"
cat "$OUT_DIR/pressure-gates.tsv"
echo "PASS microagent-e2e-model-mediation-pressure: pressure matrix passed with $LABEL"
