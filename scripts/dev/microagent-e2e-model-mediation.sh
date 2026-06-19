#!/usr/bin/env bash
#
# microagent-e2e-model-mediation.sh - opt-in production model mediation check.
#
# This validates the real `run --model` path with the experimental host-worker
# mediator enabled. It uses a stub OpenAI-compatible runner by default, so it
# does not require a GPU, llama.cpp, vLLM, or network access to HuggingFace.
#
# Required:
#   MICROAGENT_E2E_MODEL_MEDIATION=1
#
# Optional:
#   MICROAGENT_CLI                         microagent CLI (default: .build/dev/microagent)
#   MICROAGENT_FIRECRACKER                 Firecracker binary for Linux runs
#   MICROAGENT_E2E_MODEL_MEDIATION_OUT_DIR output dir
#   MICROAGENT_E2E_MODEL_MEDIATION_STATE_DIR state dir
#   MICROAGENT_E2E_MODEL_MEDIATION_KEEP    preserve reports/state
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

CLI="${MICROAGENT_CLI:-$(e2e_exe "$ROOT/.build/dev/microagent")}"
OUT_DIR="${MICROAGENT_E2E_MODEL_MEDIATION_OUT_DIR:-/tmp/microagent-e2e-model-mediation-$(date +%Y%m%d%H%M%S)}"
STATE_DIR="${MICROAGENT_E2E_MODEL_MEDIATION_STATE_DIR:-$OUT_DIR/state}"
KEEP_FAILED="${MICROAGENT_E2E_MODEL_MEDIATION_KEEP:-${MICROAGENT_KEEP_MICROAGENT_E2E_MODEL_MEDIATION:-0}}"
IMAGE="${MICROAGENT_E2E_MODEL_MEDIATION_IMAGE:-quay.io/curl/curl:latest}"
MODEL_REF="${MICROAGENT_E2E_MODEL_MEDIATION_MODEL_REF:-stub/stub-model-GGUF/stub.gguf}"
CANONICAL_REF="hf.co/stub/stub-model-GGUF@main/stub.gguf"
POLICY_PID=""
POLICY_URL=""
POLICY_FILE=""
RUN_FLAGS=(--backend linux-kvm --network isolated --state-dir "$STATE_DIR" --model "$MODEL_REF" --rm "$IMAGE")
CTRL_FLAGS=(--backend linux-kvm --state-dir "$STATE_DIR")

skip() { e2e_skip "microagent-e2e-model-mediation: $1"; }
fail() { echo "FAIL microagent-e2e-model-mediation: $1" >&2; exit 1; }

cleanup() {
  local status=$?
  set +e
  if [ -n "$POLICY_PID" ]; then
    kill "$POLICY_PID" >/dev/null 2>&1 || true
    wait "$POLICY_PID" >/dev/null 2>&1 || true
    POLICY_PID=""
  fi
  for workspace in model-med-direct model-med-local-allow model-med-policy-allow model-med-policy-deny model-med-policy-file-allow model-med-policy-file-deny model-med-pf-chat model-med-pf-tool-deny model-med-pf-stream-deny model-med-policy-unavailable; do
    "$CLI" kill "$workspace" "${CTRL_FLAGS[@]}" >/dev/null 2>&1 || true
    "$CLI" delete "$workspace" --force --yes "${CTRL_FLAGS[@]}" >/dev/null 2>&1 || true
  done
  "$CLI" model stop "$CANONICAL_REF" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  if [ "$KEEP_FAILED" = "1" ]; then
    if [ "$status" -ne 0 ]; then
      echo "microagent-e2e-model-mediation: preserved failed state under $OUT_DIR" >&2
    else
      echo "microagent-e2e-model-mediation: preserved reports under $OUT_DIR" >&2
    fi
  else
    rm -rf "$OUT_DIR"
  fi
  exit "$status"
}
trap cleanup EXIT

case "${MICROAGENT_E2E_MODEL_MEDIATION:-0}" in
  1|true|TRUE|yes|YES|required)
    ;;
  *)
    skip "set MICROAGENT_E2E_MODEL_MEDIATION=1 to run the opt-in model mediation scenario"
    ;;
esac
case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    skip "model mediation E2E currently targets the Linux host backend"
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

mkdir -p "$OUT_DIR/bin" "$STATE_DIR/models/blobs"

write_stub_engine() {
  local dir="$1"
  mkdir -p "$dir"
  cat >"$dir/go.mod" <<'EOF_GO_MOD'
module stubengine

go 1.26
EOF_GO_MOD
  cat >"$dir/main.go" <<'EOF_GO'
package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	host, port := "127.0.0.1", ""
	for i, arg := range os.Args {
		if arg == "--host" && i+1 < len(os.Args) {
			host = os.Args[i+1]
		}
		if arg == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	http.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"stub-model"}]}`)
	})
	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"stub-chat","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"PONG"},"finish_reason":"stop"}]}`)
	})
	if err := http.ListenAndServe(host+":"+port, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
EOF_GO
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

write_file_policy() {
  local path="$1"
  local decision="$2"
  cat >"$path" <<EOF
{
  "schema_version": "microagent.model_policy.v1",
  "default": "deny",
  "rules": [
    {
      "id": "models",
      "effect": "$decision",
      "match": {
        "methods": ["GET"],
        "paths": ["/v1/models"]
      }
    }
  ]
}
EOF
}

write_chat_file_policy() {
  local path="$1"
  cat >"$path" <<'EOF'
{
  "schema_version": "microagent.model_policy.v1",
  "default": "deny",
  "rules": [
    {
      "id": "chat",
      "effect": "allow",
      "match": {
        "methods": ["POST"],
        "paths": ["/v1/chat/completions"],
        "models": ["stub-model"]
      },
      "limits": {
        "max_request_bytes": 4096,
        "max_text_bytes": 128,
        "max_messages": 2,
        "max_tokens": 16,
        "stream": false,
        "allowed_tool_names": ["shell"]
      }
    }
  ]
}
EOF
}

audit_log_for_workspace() {
  local workspace="$1"
  printf '%s\n' "$STATE_DIR/host-workers/${workspace}_model.openai.jsonl"
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

summarize_audit() {
  local label="$1"
  local workspace="$2"
  local expected="$3"
  local log_path
  log_path="$(audit_log_for_workspace "$workspace")"
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

assert_audit_contains() {
  local workspace="$1"
  local needle="$2"
  local log_path
  log_path="$(audit_log_for_workspace "$workspace")"
  [ -r "$log_path" ] || fail "audit log not readable for $workspace: $log_path"
  grep -q "\"event\":\"$needle\"" "$log_path" || fail "audit log $log_path missing event $needle"
}

assert_audit_lacks() {
  local workspace="$1"
  local needle="$2"
  local log_path
  log_path="$(audit_log_for_workspace "$workspace")"
  [ -r "$log_path" ] || fail "audit log not readable for $workspace: $log_path"
  if grep -q "\"event\":\"$needle\"" "$log_path"; then
    fail "audit log $log_path unexpectedly contains event $needle"
  fi
}

run_case() {
  local label="$1"
  local mode="$2"
  local expected_status="$3"
  local workspace="model-med-$label"
  local run_log="$OUT_DIR/$label.run.log"
  local env_args=(
    "MICROAGENT_LLAMA_SERVER=$ENGINE"
    "MICROAGENT_MODEL_RUNNER_ARGS="
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

  echo "microagent-e2e-model-mediation: case=$label mode=$mode expected_http=$expected_status"
  # shellcheck disable=SC2016
  if ! env "${env_args[@]}" "$CLI" run --name "$workspace" --env "EXPECTED_STATUS=$expected_status" "${RUN_FLAGS[@]}" sh -c \
    'code="$(curl -sS -o /tmp/model-body -w "%{http_code}" "$MICROAGENT_MODEL_URL/models" || true)"; cat /tmp/model-body; echo; echo "HTTP_STATUS=$code"; test "$code" = "$EXPECTED_STATUS"' >"$run_log" 2>&1; then
    cat "$run_log" >&2
    fail "case $label failed"
  fi
  grep -q "HTTP_STATUS=$expected_status" "$run_log" || {
    cat "$run_log" >&2
    fail "case $label did not report expected status"
  }
  if [ "$expected_status" = "200" ]; then
    grep -q "stub-model" "$run_log" || {
      cat "$run_log" >&2
      fail "case $label did not reach stub model"
    }
  fi
  if [ "$mode" != "off" ]; then
    assert_audit_contains "$workspace" "request_end"
  fi
  assert_index_clean
  summarize_audit "$label" "$workspace" "$expected_status"
}

run_chat_case() {
  local label="$1"
  local expected_status="$2"
  local request_body="$3"
  local workspace="model-med-$label"
  local run_log="$OUT_DIR/$label.run.log"
  local env_args=(
    "MICROAGENT_LLAMA_SERVER=$ENGINE"
    "MICROAGENT_MODEL_RUNNER_ARGS="
    "MICROAGENT_MODEL_MEDIATION=policy"
    "MICROAGENT_MODEL_POLICY_TIMEOUT=1s"
    "MICROAGENT_MODEL_POLICY_URL="
    "MICROAGENT_MODEL_POLICY_FILE=$POLICY_FILE"
  )

  echo "microagent-e2e-model-mediation: case=$label mode=policy expected_http=$expected_status"
  # shellcheck disable=SC2016
  if ! env "${env_args[@]}" "$CLI" run --name "$workspace" --env "EXPECTED_STATUS=$expected_status" --env "REQUEST_BODY=$request_body" "${RUN_FLAGS[@]}" sh -c \
    'printf "%s" "$REQUEST_BODY" >/tmp/request.json; code="$(curl -sS -o /tmp/model-body -w "%{http_code}" -H "Content-Type: application/json" --data-binary @/tmp/request.json "$MICROAGENT_MODEL_URL/chat/completions" || true)"; cat /tmp/model-body; echo; echo "HTTP_STATUS=$code"; test "$code" = "$EXPECTED_STATUS"' >"$run_log" 2>&1; then
    cat "$run_log" >&2
    fail "case $label failed"
  fi
  grep -q "HTTP_STATUS=$expected_status" "$run_log" || {
    cat "$run_log" >&2
    fail "case $label did not report expected status"
  }
  if [ "$expected_status" = "200" ]; then
    grep -q "stub-chat" "$run_log" || {
      cat "$run_log" >&2
      fail "case $label did not reach stub chat endpoint"
    }
  fi
  assert_audit_contains "$workspace" "request_end"
  assert_index_clean
  summarize_audit "$label" "$workspace" "$expected_status"
}

echo "microagent-e2e-model-mediation: building stub runner"
write_stub_engine "$OUT_DIR/stub-engine"
ENGINE="$OUT_DIR/bin/stub-engine"
(cd "$OUT_DIR/stub-engine" && go build -buildvcs=false -o "$ENGINE" .) || fail "build stub runner failed"
stage_stub_model

run_case "direct" "off" "200"
run_case "local-allow" "local-allow" "200"
assert_audit_contains "model-med-local-allow" "mediation_decision_allow"
assert_audit_contains "model-med-local-allow" "upstream_headers"

start_policy allow "allow"
run_case "policy-allow" "policy" "200"
assert_audit_contains "model-med-policy-allow" "mediation_decision_allow"
assert_audit_contains "model-med-policy-allow" "upstream_headers"
stop_policy

start_policy deny "deny"
run_case "policy-deny" "policy" "403"
assert_audit_contains "model-med-policy-deny" "mediation_decision_deny"
assert_audit_lacks "model-med-policy-deny" "upstream_headers"
stop_policy

POLICY_FILE="$OUT_DIR/policy-file-allow.json"
write_file_policy "$POLICY_FILE" "allow"
run_case "policy-file-allow" "policy" "200"
assert_audit_contains "model-med-policy-file-allow" "mediation_decision_allow"
assert_audit_contains "model-med-policy-file-allow" "upstream_headers"

POLICY_FILE="$OUT_DIR/policy-file-deny.json"
write_file_policy "$POLICY_FILE" "deny"
run_case "policy-file-deny" "policy" "403"
assert_audit_contains "model-med-policy-file-deny" "mediation_decision_deny"
assert_audit_lacks "model-med-policy-file-deny" "upstream_headers"
POLICY_FILE=""

POLICY_FILE="$OUT_DIR/policy-file-chat.json"
write_chat_file_policy "$POLICY_FILE"
run_chat_case "pf-chat" "200" '{"model":"stub-model","stream":false,"max_tokens":8,"messages":[{"role":"user","content":"ping"}],"tools":[{"type":"function","function":{"name":"shell"}}]}'
assert_audit_contains "model-med-pf-chat" "mediation_decision_allow"
assert_audit_contains "model-med-pf-chat" "upstream_headers"
run_chat_case "pf-tool-deny" "403" '{"model":"stub-model","stream":false,"max_tokens":8,"messages":[{"role":"user","content":"ping"}],"tools":[{"type":"function","function":{"name":"network"}}]}'
assert_audit_contains "model-med-pf-tool-deny" "mediation_decision_deny"
assert_audit_lacks "model-med-pf-tool-deny" "upstream_headers"
run_chat_case "pf-stream-deny" "403" '{"model":"stub-model","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"ping"}]}'
assert_audit_contains "model-med-pf-stream-deny" "mediation_decision_deny"
assert_audit_lacks "model-med-pf-stream-deny" "upstream_headers"
POLICY_FILE=""

unavailable_port="$(choose_port)"
POLICY_URL="http://127.0.0.1:${unavailable_port}/decision"
run_case "policy-unavailable" "policy" "503"
assert_audit_contains "model-med-policy-unavailable" "mediation_decision_error"
assert_audit_lacks "model-med-policy-unavailable" "upstream_headers"
POLICY_URL=""

echo "microagent-e2e-model-mediation: summary"
cat "$OUT_DIR/summary.tsv"
echo "PASS microagent-e2e-model-mediation: production model mediation matrix passed"
