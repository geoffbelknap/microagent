#!/usr/bin/env bash
#
# microagent-host-worker-slot-sweep.sh - run host-worker probes across runner slot counts.
#
# This wrapper is intentionally llama.cpp-oriented because it is an experiment
# harness for local slot/parallelism tuning. The probe it drives remains
# OpenAI-compatible and runner-neutral.
#
# Required:
#   MICROAGENT_LLAMA_SERVER                      CUDA-capable llama-server path
#
# Optional:
#   MICROAGENT_CLI                               microagent CLI (default: .build/dev/microagent)
#   MICROAGENT_FIRECRACKER                       Firecracker binary for Linux runs
#   MICROAGENT_HOST_WORKER_SLOT_SWEEP_MODEL_REF  HuggingFace GGUF ref
#   MICROAGENT_HOST_WORKER_SLOT_SWEEP_SLOTS      comma-separated slot counts (default: 1,2,4)
#   MICROAGENT_HOST_WORKER_SLOT_SWEEP_OUT_DIR    report directory (default: /tmp/microagent-host-worker-slot-sweep-$$)
#   MICROAGENT_HOST_WORKER_SLOT_SWEEP_EXTRA_ARGS JSON array or shell words appended to llama-server args
#   MICROAGENT_HOST_WORKER_SLOT_SWEEP_SKIP_PULL  skip model pull: 0/1 (default: 0)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

CLI="${MICROAGENT_CLI:-$(e2e_exe "$ROOT/.build/dev/microagent")}"
MODEL_REF="${MICROAGENT_HOST_WORKER_SLOT_SWEEP_MODEL_REF:-${MICROAGENT_HOST_WORKER_PROBE_MODEL_REF:-Qwen/Qwen2.5-0.5B-Instruct-GGUF/qwen2.5-0.5b-instruct-q4_k_m.gguf}}"
SLOTS="${MICROAGENT_HOST_WORKER_SLOT_SWEEP_SLOTS:-1,2,4}"
OUT_DIR="${MICROAGENT_HOST_WORKER_SLOT_SWEEP_OUT_DIR:-/tmp/microagent-host-worker-slot-sweep-$$}"
EXTRA_ARGS="${MICROAGENT_HOST_WORKER_SLOT_SWEEP_EXTRA_ARGS:-}"
SKIP_PULL="${MICROAGENT_HOST_WORKER_SLOT_SWEEP_SKIP_PULL:-0}"
STATE_DIR="${MICROAGENT_HOST_WORKER_PROBE_STATE_DIR:-$HOME/.microagent}"
ACTIVE_RUNNER=0

skip() { e2e_skip "microagent-host-worker-slot-sweep: $1"; }
fail() { echo "FAIL microagent-host-worker-slot-sweep: $1" >&2; exit 1; }

cleanup() {
  local status=$?
  set +e
  if [ "$ACTIVE_RUNNER" -eq 1 ]; then
    "$CLI" model stop "$MODEL_REF" --state-dir "$STATE_DIR" >/dev/null 2>&1
  fi
  exit "$status"
}
trap cleanup EXIT

runner_count_for_model() {
  local runners_json
  runners_json="$("$CLI" --json model runners --state-dir "$STATE_DIR" 2>/dev/null || printf '{}')"
  python3 - "$MODEL_REF" "$runners_json" <<'PY'
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

runner_args_json() {
  local slots="$1"
  python3 - "$slots" "$EXTRA_ARGS" <<'PY'
import json
import shlex
import sys

slots = sys.argv[1]
extra = sys.argv[2].strip()
args = ["-ngl", "all", "--no-ui", "--metrics", "--slots", "--parallel", slots]
if extra:
    try:
        loaded = json.loads(extra)
    except json.JSONDecodeError:
        loaded = shlex.split(extra)
    if not isinstance(loaded, list) or not all(isinstance(item, str) for item in loaded):
        raise SystemExit("extra runner args must be a JSON string array or shell words")
    args.extend(loaded)
print(json.dumps(args, separators=(",", ":")))
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

case "$SKIP_PULL" in
  0|1)
    ;;
  *)
    fail "MICROAGENT_HOST_WORKER_SLOT_SWEEP_SKIP_PULL must be 0 or 1"
    ;;
esac

if [ ! -x "$CLI" ]; then
  skip "CLI not found at $CLI (run scripts/dev/build-local.sh)"
fi
if [ -z "${MICROAGENT_LLAMA_SERVER:-}" ] || [ ! -x "${MICROAGENT_LLAMA_SERVER:-/nonexistent}" ]; then
  skip "MICROAGENT_LLAMA_SERVER not set/executable"
fi

SLOTS_SPACES="$(printf '%s' "$SLOTS" | tr ',' ' ')"
if [ -z "$(printf '%s' "$SLOTS_SPACES" | tr -d '[:space:]')" ]; then
  fail "MICROAGENT_HOST_WORKER_SLOT_SWEEP_SLOTS must include at least one positive integer"
fi
for slots in $SLOTS_SPACES; do
  case "$slots" in
    ''|*[!0-9]*) fail "MICROAGENT_HOST_WORKER_SLOT_SWEEP_SLOTS must be comma-separated positive integers" ;;
  esac
  if [ "$slots" -le 0 ]; then
    fail "MICROAGENT_HOST_WORKER_SLOT_SWEEP_SLOTS values must be > 0"
  fi
done

existing_count="$(runner_count_for_model)"
if [ "$existing_count" -ne 0 ]; then
  skip "model $MODEL_REF already has $existing_count active runner(s); stop them before running the sweep"
fi

mkdir -p "$OUT_DIR"
if [ "$SKIP_PULL" -eq 0 ]; then
  echo "microagent-host-worker-slot-sweep: pulling or refreshing model record"
  "$CLI" --json model pull "$MODEL_REF" --state-dir "$STATE_DIR" >/dev/null || fail "model pull failed"
fi

reports=()
for slots in $SLOTS_SPACES; do
  echo "microagent-host-worker-slot-sweep: starting runner with slots=$slots"
  export MICROAGENT_MODEL_RUNNER_ARGS
  MICROAGENT_MODEL_RUNNER_ARGS="$(runner_args_json "$slots")"
  runner_json="$("$CLI" --json model serve "$MODEL_REF" --state-dir "$STATE_DIR")" || fail "model serve failed for slots=$slots"
  ACTIVE_RUNNER=1
  base_url="$(runner_base_url "$runner_json")"
  report="$OUT_DIR/slots-$slots.json"
  reports+=("$report")

  echo "microagent-host-worker-slot-sweep: probing slots=$slots at $base_url"
  MICROAGENT_HOST_WORKER_URL="$base_url" \
    MICROAGENT_HOST_WORKER_LABEL="slots-$slots" \
    MICROAGENT_HOST_WORKER_SLOTS="$slots" \
    MICROAGENT_HOST_WORKER_PROBE_REPORT="$report" \
    "$ROOT/scripts/dev/microagent-host-worker-probe.sh" || fail "probe failed for slots=$slots"

  "$CLI" model stop "$MODEL_REF" --state-dir "$STATE_DIR" >/dev/null || fail "model stop failed for slots=$slots"
  ACTIVE_RUNNER=0
done

echo "microagent-host-worker-slot-sweep: reports written under $OUT_DIR"
"$ROOT/scripts/dev/microagent-host-worker-report-summary.py" "${reports[@]}" >"$OUT_DIR/summary.tsv"
cat "$OUT_DIR/summary.tsv"
"$ROOT/scripts/dev/microagent-host-worker-report-summary.py" --pressure "${reports[@]}" >"$OUT_DIR/pressure.tsv"
cat "$OUT_DIR/pressure.tsv"
echo "PASS microagent-host-worker-slot-sweep"
