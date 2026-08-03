#!/usr/bin/env bash
#
# microagent-e2e-model-mediation-runner-fake.sh - opt-in runner-neutral
# production model mediation check against a local fake OpenAI-compatible
# runner.
#
# This validates the shared runner-neutral mediation harness through
# `microagent model serve` custom runner configuration. It needs a microVM
# backend, but it does not need llama.cpp, vLLM, a GPU, or HuggingFace network
# access.
#
# Required:
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE=1
#
# Optional:
#   MICROAGENT_CLI
#   MICROAGENT_FIRECRACKER
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_IMAGE
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_OUT_DIR
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_STATE_DIR
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_KEEP
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_KEEP_STATE
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_CHAT_TOKENS
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_STREAM_TOKENS
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_SAMPLES
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_TELEMETRY
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_GATE_MODE
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_PRESSURE
#   MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_PRESSURE_PRESET
#       default, baseline, ci, or hardware
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"
# shellcheck source=scripts/dev/microagent-model-mediation-pressure-presets.sh disable=SC1091
. "$ROOT/scripts/dev/microagent-model-mediation-pressure-presets.sh"

CLI="${MICROAGENT_CLI:-$(e2e_exe "$ROOT/.build/dev/microagent")}"
# Probe workspaces boot a microVM, which needs a guest kernel. A dev box already
# has one; a fresh CI runner does not, so install the default backend's kernel
# (idempotent, auto-detects the platform backend) and skip if it cannot be
# fetched — matching how the core scenarios provision their kernel.
if ! "$CLI" kernel install >/dev/null 2>&1; then
  e2e_skip "kernel install failed (no kernel for probe microVMs)"
fi
OUT_DIR="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_OUT_DIR:-/tmp/microagent-e2e-model-mediation-runner-fake-$(date +%Y%m%d%H%M%S)}"
STATE_DIR="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_STATE_DIR:-$OUT_DIR/state}"
KEEP_FAILED="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_KEEP:-${MICROAGENT_KEEP_MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE:-0}}"
KEEP_STATE="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_KEEP_STATE:-0}"
IMAGE="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_IMAGE:-quay.io/curl/curl:latest}"
MODEL_REF="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_MODEL_REF:-stub/stub-model-GGUF/stub.gguf}"
CANONICAL_REF="hf.co/stub/stub-model-GGUF@main/stub.gguf"
REQUEST_MODEL="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_REQUEST_MODEL:-stub-model}"
CHAT_TOKENS="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_CHAT_TOKENS:-32}"
STREAM_TOKENS="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_STREAM_TOKENS:-32}"
SAMPLES="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_SAMPLES:-1}"
TELEMETRY="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_TELEMETRY:-off}"
TELEMETRY_INTERVAL="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_TELEMETRY_INTERVAL:-0.5}"
TELEMETRY_ENDPOINTS="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_TELEMETRY_ENDPOINTS:-/metrics,/health}"
GATE_MODE="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_GATE_MODE:-required}"
PRESSURE="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_PRESSURE:-0}"
PRESSURE_PRESET="${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_PRESSURE_PRESET:-default}"
case "$PRESSURE" in
  1|true|TRUE|yes|YES|required) ;;
  *) PRESSURE_PRESET=default ;;
esac
PRESSURE_PREFIX="MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE"
PRESSURE_WORKSPACES="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_WORKSPACES 2)"
PRESSURE_CONCURRENCY="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_CONCURRENCY 1,2)"
PRESSURE_CASES="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_CASES direct,local,pf,pa)"
PRESSURE_SAMPLES="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_SAMPLES "$SAMPLES")"
PRESSURE_WARMUPS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_WARMUPS 1)"
PRESSURE_CHAT_TOKENS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_CHAT_TOKENS "$CHAT_TOKENS")"
PRESSURE_STREAM_TOKENS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_STREAM_TOKENS "$STREAM_TOKENS")"
PRESSURE_GATE_MODE="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_GATE_MODE warn)"
PRESSURE_TELEMETRY="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_TELEMETRY "$TELEMETRY")"
MAX_MODELS_TOTAL_P95_DELTA_MS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_MAX_MODELS_TOTAL_P95_DELTA_MS "${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_MAX_MODELS_TOTAL_P95_DELTA_MS:-100}")"
MAX_CHAT_TOTAL_P95_DELTA_MS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_MAX_CHAT_TOTAL_P95_DELTA_MS "${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_MAX_CHAT_TOTAL_P95_DELTA_MS:-500}")"
MAX_STREAM_TTFB_P95_DELTA_MS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_MAX_STREAM_TTFB_P95_DELTA_MS "${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_MAX_STREAM_TTFB_P95_DELTA_MS:-250}")"
MAX_DECISION_P95_MS="$(pressure_preset_value "$PRESSURE_PREFIX" "$PRESSURE_PRESET" PRESSURE_MAX_DECISION_P95_MS "${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_MAX_DECISION_P95_MS:-100}")"
PYTHON="${PYTHON:-python3}"
RUNNER_COMMAND_JSON=""
RUNNER_ENV_JSON=""
RUNNER_ARGS_JSON=""
RUNNER_PID=""
RUNNER_PORT=""
CTRL_FLAGS=(--backend linux-kvm --state-dir "$STATE_DIR")

skip() { e2e_skip "microagent-e2e-model-mediation-runner-fake: $1"; }
fail() { echo "FAIL microagent-e2e-model-mediation-runner-fake: $1" >&2; exit 1; }

cleanup() {
  local status=$?
  set +e
  if [ "$KEEP_STATE" = "1" ] && [ "$status" -ne 0 ]; then
    echo "microagent-e2e-model-mediation-runner-fake: preserved workspace state under $STATE_DIR" >&2
  else
    for workspace in mmf-direct mmf-local mmf-pa mmf-pd mmf-pf mmf-pfd mmf-pu; do
      "$CLI" kill "$workspace" "${CTRL_FLAGS[@]}" --reason "fake runner E2E cleanup" --yes >/dev/null 2>&1 || true
      "$CLI" delete "$workspace" --force --yes "${CTRL_FLAGS[@]}" >/dev/null 2>&1 || true
    done
  fi
  "$CLI" model stop "$CANONICAL_REF" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  if [ "$KEEP_FAILED" = "1" ]; then
    if [ "$status" -ne 0 ]; then
      echo "microagent-e2e-model-mediation-runner-fake: preserved failed state under $OUT_DIR" >&2
    else
      echo "microagent-e2e-model-mediation-runner-fake: preserved reports under $OUT_DIR" >&2
    fi
  else
    rm -rf "$OUT_DIR"
  fi
  exit "$status"
}
trap cleanup EXIT

case "${MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE:-0}" in
  1|true|TRUE|yes|YES|required)
    ;;
  *)
    skip "set MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE=1 to run the fake runner-neutral model mediation scenario"
    ;;
esac
case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    skip "fake runner-neutral model mediation E2E currently targets the Linux host backend"
    ;;
esac
if [ ! -x "$CLI" ]; then
  skip "CLI not found at $CLI (run scripts/dev/build-local.sh)"
fi
if [ ! -e /dev/kvm ]; then
  skip "/dev/kvm not available"
fi
if ! pressure_preset_validate "$PRESSURE_PRESET"; then
  fail "MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_PRESSURE_PRESET must be default, baseline, ci, or hardware"
fi
if [ -z "${MICROAGENT_FIRECRACKER:-}" ]; then
  MICROAGENT_FIRECRACKER="$(e2e_resolve_firecracker)" || skip "Firecracker binary not resolved"
  export MICROAGENT_FIRECRACKER
fi
if ! command -v "$PYTHON" >/dev/null 2>&1; then
  skip "$PYTHON not found"
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
  *) fail "MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_TELEMETRY must be off, auto, or required" ;;
esac
case "$GATE_MODE" in
  off|warn|required) ;;
  *) fail "MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_GATE_MODE must be off, warn, or required" ;;
esac
case "$KEEP_STATE" in
  1|true|TRUE|yes|YES)
    KEEP_STATE=1
    ;;
  *)
    KEEP_STATE=0
    ;;
esac

mkdir -p "$OUT_DIR/bin" "$STATE_DIR/models/blobs"

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

write_fake_runner() {
  local runner="$OUT_DIR/bin/fake-openai-runner.py"
  cat >"$runner" <<'PY'
#!/usr/bin/env python3
import argparse
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Handler(BaseHTTPRequestHandler):
    server_version = "microagent-fake-openai/1"

    def log_message(self, fmt, *args):
        return

    @property
    def model_id(self):
        return os.environ.get("MICROAGENT_FAKE_MODEL_ID", "stub-model")

    def send_json(self, status, payload):
        body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/health":
            self.send_response(200)
            self.end_headers()
            return
        if self.path == "/metrics":
            body = b"microagent_fake_runner_requests_total 1\n"
            self.send_response(200)
            self.send_header("Content-Type", "text/plain; version=0.0.4")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if self.path == "/v1/models":
            self.send_json(200, {"object": "list", "data": [{"id": self.model_id}]})
            return
        self.send_json(404, {"error": {"message": "not found"}})

    def do_POST(self):
        if self.path != "/v1/chat/completions":
            self.send_json(404, {"error": {"message": "not found"}})
            return
        length = int(self.headers.get("Content-Length") or "0")
        raw = self.rfile.read(length) if length else b"{}"
        try:
            request = json.loads(raw.decode("utf-8"))
        except Exception:
            request = {}
        if bool(request.get("stream")):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            chunk = {
                "id": "stub-stream",
                "object": "chat.completion.chunk",
                "choices": [
                    {
                        "index": 0,
                        "delta": {"role": "assistant", "content": "PONG"},
                        "finish_reason": None,
                    }
                ],
            }
            self.wfile.write(b"data: " + json.dumps(chunk, separators=(",", ":")).encode("utf-8") + b"\n\n")
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
            return
        self.send_json(
            200,
            {
                "id": "stub-chat",
                "object": "chat.completion",
                "choices": [
                    {
                        "index": 0,
                        "message": {"role": "assistant", "content": "PONG"},
                        "finish_reason": "stop",
                    }
                ],
            },
        )


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--model-path")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--metrics-label", default="fake")
    args = parser.parse_args()
    del args.metrics_label
    ThreadingHTTPServer((args.host, args.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
PY
  chmod +x "$runner"
  RUNNER_COMMAND_JSON="$(python3 - "$PYTHON" "$runner" <<'PY'
import json
import sys

print(json.dumps([sys.argv[1], sys.argv[2], "--model-path", "{model}", "--host", "{host}", "--port", "{port}"]))
PY
)"
  RUNNER_ENV_JSON="$(python3 - "$REQUEST_MODEL" <<'PY'
import json
import sys

print(json.dumps({"MICROAGENT_FAKE_MODEL_ID": sys.argv[1]}, separators=(",", ":")))
PY
)"
  RUNNER_ARGS_JSON='["--metrics-label","fake"]'
}

runner_env_args() {
  printf '%s\n' \
    "MICROAGENT_MODEL_RUNNER_COMMAND=$RUNNER_COMMAND_JSON" \
    "MICROAGENT_MODEL_RUNNER_NAME=fake-openai" \
    "MICROAGENT_MODEL_RUNNER_HEALTH_PATH=/health" \
    "MICROAGENT_MODEL_RUNNER_ENV=$RUNNER_ENV_JSON" \
    "MICROAGENT_MODEL_RUNNER_ARGS=$RUNNER_ARGS_JSON"
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
  echo "microagent-e2e-model-mediation-runner-fake: pinned fake runner pid=$RUNNER_PID model=$REQUEST_MODEL"
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
if runner.get("engine") != "fake-openai":
    raise SystemExit(f"runner engine = {runner.get('engine')!r}, want 'fake-openai'")
PY
}

stage_stub_model
write_fake_runner
start_pinned_runner
assert_single_runner_reused

RUNNER_ENV_FILE="$OUT_DIR/runner-env.env"
runner_env_args >"$RUNNER_ENV_FILE"

case "$PRESSURE" in
  1|true|TRUE|yes|YES|required)
    MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE=1 \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_LABEL="fake-openai" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_CASE_PREFIX="mmf" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_OUT_DIR="$OUT_DIR" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_OWN_OUT_DIR=0 \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_STATE_DIR="$STATE_DIR" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_IMAGE="$IMAGE" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_MODEL_REF="$MODEL_REF" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_ENV_FILE="$RUNNER_ENV_FILE" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_PID="$RUNNER_PID" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_PORT="$RUNNER_PORT" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_RUNNER_ENGINE="fake-openai" \
      MICROAGENT_E2E_MODEL_MEDIATION_PRESSURE_REQUEST_MODEL="$REQUEST_MODEL" \
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
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_LABEL="fake-openai" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_CASE_PREFIX="mmf" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_OUT_DIR="$OUT_DIR" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_OWN_OUT_DIR=0 \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_STATE_DIR="$STATE_DIR" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_IMAGE="$IMAGE" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_MODEL_REF="$MODEL_REF" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_ENV_FILE="$RUNNER_ENV_FILE" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_PID="$RUNNER_PID" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_PORT="$RUNNER_PORT" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_RUNNER_ENGINE="fake-openai" \
      MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_REQUEST_MODEL="$REQUEST_MODEL" \
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
    fail "MICROAGENT_E2E_MODEL_MEDIATION_RUNNER_FAKE_PRESSURE must be 0/1, true/false, or yes/no"
    ;;
esac
