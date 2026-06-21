#!/usr/bin/env bash
#
# microagent-e2e-model-mediation-runner.sh - opt-in runner-neutral production
# model mediation check against an OpenAI-compatible host runner.
#
# This validates the real `run --model` path with the experimental host-worker
# mediator enabled. The runner can be provided by an adapter script that has
# already pinned it with `microagent model serve`, or this script can start it
# directly from MICROAGENT_MODEL_RUNNER_* configuration.
#
# Required for the live runner matrix:
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER=1
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_MODEL_REF
#
# Required for policy-only smoke:
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_POLICY_ONLY=1
#
# Optional:
#   MICROAGENT_CLI
#   MICROAGENT_FIRECRACKER
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_OUT_DIR
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_STATE_DIR
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_IMAGE
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_LABEL
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_CASE_PREFIX
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_ENV_FILE
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_PID
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_PORT
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_ENGINE
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_REQUEST_MODEL
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_KEEP_STATE
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_POLICY_ONLY
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_OWN_OUT_DIR
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

CLI="${MICROAGENT_CLI:-$(e2e_exe "$ROOT/.build/dev/microagent")}"
LABEL="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_LABEL:-runner}"
CASE_PREFIX="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_CASE_PREFIX:-mmr}"
OUT_DIR="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_OUT_DIR:-/tmp/microagent-e2e-model-mediation-runner-$(date +%Y%m%d%H%M%S)}"
STATE_DIR="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_STATE_DIR:-$OUT_DIR/state}"
IMAGE="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_IMAGE:-quay.io/curl/curl:latest}"
MODEL_REF="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_MODEL_REF:-}"
RUNNER_ENV_FILE="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_ENV_FILE:-}"
RUNNER_PID="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_PID:-}"
RUNNER_PORT="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_PORT:-}"
RUNNER_ENGINE="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_ENGINE:-}"
REQUEST_MODEL="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_REQUEST_MODEL:-}"
POLICY_ONLY="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_POLICY_ONLY:-0}"
OWN_OUT_DIR="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_OWN_OUT_DIR:-1}"
KEEP_FAILED="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_KEEP:-${MICROAGENT_KEEP_MICROAGENT_E2E_MODEL_MEDIATION_RUNNER:-0}}"
KEEP_STATE="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_KEEP_STATE:-0}"
CHAT_TOKENS="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_CHAT_TOKENS:-64}"
STREAM_TOKENS="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_STREAM_TOKENS:-96}"
SAMPLES="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_SAMPLES:-3}"
TELEMETRY="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_TELEMETRY:-auto}"
TELEMETRY_INTERVAL="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_TELEMETRY_INTERVAL:-0.5}"
TELEMETRY_ENDPOINTS="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_TELEMETRY_ENDPOINTS:-/metrics,/health}"
GATE_MODE="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_GATE_MODE:-required}"
MAX_MODELS_TOTAL_P95_DELTA_MS="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_MAX_MODELS_TOTAL_P95_DELTA_MS:-100}"
MAX_CHAT_TOTAL_P95_DELTA_MS="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_MAX_CHAT_TOTAL_P95_DELTA_MS:-500}"
MAX_STREAM_TTFB_P95_DELTA_MS="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_MAX_STREAM_TTFB_P95_DELTA_MS:-250}"
MAX_DECISION_P95_MS="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_MAX_DECISION_P95_MS:-100}"
POLICY_PID=""
POLICY_URL=""
POLICY_FILE=""
STARTED_RUNNER=0
TELEMETRY_PID=""
TELEMETRY_PHASE_FILE=""

skip() { e2e_skip "microagent-e2e-model-mediation-runner: $1"; }
fail() { echo "FAIL microagent-e2e-model-mediation-runner: $1" >&2; exit 1; }

usage() {
  cat <<'EOF'
microagent-e2e-model-mediation-runner.sh

Runner-neutral production model mediation E2E for OpenAI-compatible host
runners.

Live matrix:
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER=1 \
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_MODEL_REF=org/repo/model.gguf \
  MICROAGENT_MODEL_RUNNER_COMMAND='runner serve {model} --host {host} --port {port}' \
  MICROAGENT_MODEL_RUNNER_NAME=runner \
  scripts/dev/microagent-e2e-model-mediation-runner.sh

Policy-only smoke, no VM or model runner:
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_POLICY_ONLY=1 \
  scripts/dev/microagent-e2e-model-mediation-runner.sh

Adapter handoff:
  An adapter may pre-start a pinned runner and pass
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_PID,
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_PORT,
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_ENGINE, and
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_REQUEST_MODEL. The shared harness then
  owns the direct/local/policy/file-policy/unavailable request matrix. Adapters
  that pass their own output directory should set
  MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_OWN_OUT_DIR=0 so they can stop their
  pinned runner before deleting or preserving state.
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
  set +e
  stop_policy
  stop_telemetry
  if [ "$STARTED_RUNNER" = "1" ] && [ -n "$MODEL_REF" ]; then
    "$CLI" model stop "$MODEL_REF" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  fi
  if [ "$OWN_OUT_DIR" != "1" ]; then
    exit "$status"
  fi
  if [ "$KEEP_FAILED" = "1" ]; then
    if [ "$status" -ne 0 ]; then
      echo "microagent-e2e-model-mediation-runner: preserved failed state under $OUT_DIR" >&2
    else
      echo "microagent-e2e-model-mediation-runner: preserved reports under $OUT_DIR" >&2
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
      fail "MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_TELEMETRY must be off, auto, or required"
      ;;
  esac
  [ -n "$RUNNER_PORT" ] || fail "runner port is required for telemetry"
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
  echo "microagent-e2e-model-mediation-runner: telemetry writing to $OUT_DIR/runner-telemetry.jsonl and $OUT_DIR/gpu-telemetry.csv"
}

write_telemetry_summary() {
  if [ "$TELEMETRY" = "off" ]; then
    return
  fi
  stop_telemetry
  python3 "$ROOT/scripts/dev/microagent-model-mediation-telemetry.py" summary \
    --runner-in "$OUT_DIR/runner-telemetry.jsonl" \
    --gpu-in "$OUT_DIR/gpu-telemetry.csv" \
    --adapter "$LABEL" \
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
  echo "microagent-e2e-model-mediation-runner: pinned $LABEL runner pid=$RUNNER_PID model=$REQUEST_MODEL"
}

assert_single_runner_reused() {
  local runners_json="$OUT_DIR/runners.json"
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
    for case in ["direct", "local", "pa", "pd", "pf", "pfd", "pu"]:
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
        for case in ["local", "pa", "pf"]:
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

write_file_policy() {
  local path="$1"
  local decision="$2"
  if [ "$decision" = "allow" ]; then
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
  else
    cat >"$path" <<'EOF'
{
  "schema_version": "microagent.model_policy.v1",
  "default": "deny",
  "rules": []
}
EOF
  fi
}

validate_file_policy() {
  local path="$1"
  local label="$2"
  local expected="$3"
  "$CLI" --json model policy validate "$path" >"$OUT_DIR/policy-$label-validate.json"
  "$CLI" --json model policy evaluate "$path" \
    --method GET \
    --path /v1/models \
    --expect "$expected" >"$OUT_DIR/policy-$label-models-evaluate.json"
  "$CLI" --json model policy evaluate "$path" \
    --method POST \
    --path /v1/chat/completions \
    --model "$REQUEST_MODEL" \
    --max-tokens "$CHAT_TOKENS" \
    --stream false \
    --text-bytes 24 \
    --messages 1 \
    --expect "$expected" >"$OUT_DIR/policy-$label-chat-evaluate.json"
  if [ "$expected" = "allow" ]; then
    "$CLI" --json model policy evaluate "$path" \
      --method POST \
      --path /v1/chat/completions \
      --model "$REQUEST_MODEL" \
      --max-tokens 4097 \
      --stream false \
      --text-bytes 24 \
      --messages 1 \
      --expect deny >"$OUT_DIR/policy-$label-chat-over-max-tokens-evaluate.json"
  fi
}

run_policy_only_smoke() {
  if [ ! -x "$CLI" ]; then
    skip "CLI not found at $CLI (run scripts/dev/build-local.sh)"
  fi
  mkdir -p "$OUT_DIR"
  if [ -z "$REQUEST_MODEL" ]; then
    REQUEST_MODEL="policy-smoke-model"
  fi
  POLICY_FILE="$OUT_DIR/policy-file-allow.json"
  write_file_policy "$POLICY_FILE" "allow"
  validate_file_policy "$POLICY_FILE" "allow" "allow"

  POLICY_FILE="$OUT_DIR/policy-file-deny.json"
  write_file_policy "$POLICY_FILE" "deny"
  validate_file_policy "$POLICY_FILE" "deny" "deny"
  POLICY_FILE=""

  echo "PASS microagent-e2e-model-mediation-runner: policy-only smoke passed"
}

run_case() {
  local label="$1"
  local mode="$2"
  local expected_status="$3"
  local workspace="$CASE_PREFIX-$label"
  local run_log="$OUT_DIR/$label.run.log"
  local stdout_path="$OUT_DIR/$label.guest.stdout"
  local guest_script
  mapfile -t runner_env < <(runner_env_args)
  local env_args=(
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
    echo "MODEL_REACHED=1"
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
    echo "CHAT_COMPATIBLE=1"
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
    echo "STREAM_COMPATIBLE=1"
  fi
  sample=$((sample + 1))
done
EOF
)"

  echo "microagent-e2e-model-mediation-runner: adapter=$LABEL case=$label mode=$mode expected_http=$expected_status"
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
  for prefix in MODEL CHAT STREAM; do
    if [ "$expected_status" = "200" ]; then
      grep -q "${prefix}_" "$stdout_path" || fail "case $label missing $prefix compatibility marker"
    fi
  done
  capture_audit_log "$workspace"
  if [ "$mode" != "off" ]; then
    assert_audit_contains "$workspace" "request_end"
    assert_audit_no_prompt_body "$workspace"
  fi
  assert_index_clean
  assert_single_runner_reused
  summarize_audit "$label" "$workspace" "$expected_status"
  summarize_profiles "$label" "$expected_status" "$stdout_path"
}

case "$POLICY_ONLY" in
  1|true|TRUE|yes|YES|required)
    run_policy_only_smoke
    exit 0
    ;;
  0|false|FALSE|no|NO|'')
    ;;
  *)
    fail "MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_POLICY_ONLY must be 0/1, true/false, or yes/no"
    ;;
esac

case "${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER:-0}" in
  1|true|TRUE|yes|YES|required)
    ;;
  *)
    skip "set MICROAGENT_E2E_MODEL_MEDIATION_RUNNER=1 to run the opt-in runner-neutral model mediation scenario"
    ;;
esac
case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    skip "runner-neutral model mediation E2E currently targets the Linux host backend"
    ;;
esac
[ -n "$MODEL_REF" ] || fail "MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_MODEL_REF is required"
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
  *) fail "MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_TELEMETRY must be off, auto, or required" ;;
esac
case "$GATE_MODE" in
  off|warn|required) ;;
  *) fail "MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_GATE_MODE must be off, warn, or required" ;;
esac
case "$KEEP_STATE" in
  1|true|TRUE|yes|YES)
    KEEP_STATE=1
    ;;
  *)
    KEEP_STATE=0
    ;;
esac

mkdir -p "$OUT_DIR" "$STATE_DIR"

RUN_FLAGS=(--backend linux-kvm --network isolated --state-dir "$STATE_DIR" --model "$MODEL_REF")
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

run_case "direct" "off" "200"
run_case "local" "local-allow" "200"
assert_audit_contains "$CASE_PREFIX-local" "mediation_decision_allow"
assert_audit_contains "$CASE_PREFIX-local" "upstream_headers"

start_policy allow "allow"
run_case "pa" "policy" "200"
assert_audit_contains "$CASE_PREFIX-pa" "mediation_decision_allow"
assert_audit_contains "$CASE_PREFIX-pa" "upstream_headers"
stop_policy

start_policy deny "deny"
run_case "pd" "policy" "403"
assert_audit_contains "$CASE_PREFIX-pd" "mediation_decision_deny"
assert_audit_lacks "$CASE_PREFIX-pd" "upstream_headers"
stop_policy

POLICY_FILE="$OUT_DIR/policy-file-allow.json"
write_file_policy "$POLICY_FILE" "allow"
validate_file_policy "$POLICY_FILE" "allow" "allow"
run_case "pf" "policy" "200"
assert_audit_contains "$CASE_PREFIX-pf" "mediation_decision_allow"
assert_audit_contains "$CASE_PREFIX-pf" "upstream_headers"

POLICY_FILE="$OUT_DIR/policy-file-deny.json"
write_file_policy "$POLICY_FILE" "deny"
validate_file_policy "$POLICY_FILE" "deny" "deny"
run_case "pfd" "policy" "403"
assert_audit_contains "$CASE_PREFIX-pfd" "mediation_decision_deny"
assert_audit_lacks "$CASE_PREFIX-pfd" "upstream_headers"
POLICY_FILE=""

unavailable_port="$(choose_port)"
POLICY_URL="http://127.0.0.1:${unavailable_port}/decision"
run_case "pu" "policy" "503"
assert_audit_contains "$CASE_PREFIX-pu" "mediation_decision_error"
assert_audit_lacks "$CASE_PREFIX-pu" "upstream_headers"
POLICY_URL=""

echo "microagent-e2e-model-mediation-runner: summary"
cat "$OUT_DIR/summary.tsv"
write_profile_summaries
echo "microagent-e2e-model-mediation-runner: profiles"
cat "$OUT_DIR/profiles.tsv"
echo "microagent-e2e-model-mediation-runner: profile summary"
cat "$OUT_DIR/profile-summary.tsv"
echo "microagent-e2e-model-mediation-runner: direct-vs-mediated profile comparison"
cat "$OUT_DIR/profile-comparison.tsv"
write_telemetry_summary
if [ -s "$OUT_DIR/telemetry-summary.tsv" ]; then
  echo "microagent-e2e-model-mediation-runner: telemetry summary"
  cat "$OUT_DIR/telemetry-summary.tsv"
fi
write_gate_summary
echo "microagent-e2e-model-mediation-runner: mediation gates"
cat "$OUT_DIR/mediation-gates.tsv"
echo "PASS microagent-e2e-model-mediation-runner: production model mediation matrix passed with $LABEL"
