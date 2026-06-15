#!/usr/bin/env bash
#
# microagent-e2e-host-worker-contract.sh - opt-in host-worker contract check.
#
# This scenario is intentionally lighter than host-worker-gpu. It validates an
# existing OpenAI-compatible host worker from the host side only, so normal E2E
# runs can skip it on machines without GPUs, model runners, or microVM support.
#
# Required:
#   MICROAGENT_E2E_HOST_WORKER_CONTRACT=1
#   MICROAGENT_E2E_HOST_WORKER_URL or MICROAGENT_HOST_WORKER_URL
#
# Optional:
#   MICROAGENT_HOST_WORKER_MODEL
#   MICROAGENT_HOST_WORKER_RUNNER_ENGINE
#   MICROAGENT_HOST_WORKER_RUNNER_VERSION
#   MICROAGENT_HOST_WORKER_TELEMETRY_ENDPOINTS  default: /metrics,/slots,/health,/version
#   MICROAGENT_E2E_HOST_WORKER_CONTRACT_REPORT  report path
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

HOST_WORKER_URL="${MICROAGENT_E2E_HOST_WORKER_URL:-${MICROAGENT_HOST_WORKER_URL:-}}"
REPORT="${MICROAGENT_E2E_HOST_WORKER_CONTRACT_REPORT:-/tmp/microagent-e2e-host-worker-contract-$(date +%Y%m%d%H%M%S).json}"
REQUEST_MODEL="${MICROAGENT_HOST_WORKER_MODEL:-}"
RUNNER_ENGINE="${MICROAGENT_HOST_WORKER_RUNNER_ENGINE:-}"
RUNNER_VERSION="${MICROAGENT_HOST_WORKER_RUNNER_VERSION:-}"
TELEMETRY_ENDPOINTS="${MICROAGENT_HOST_WORKER_TELEMETRY_ENDPOINTS:-/metrics,/slots,/health,/version}"

skip() { e2e_skip "microagent-e2e-host-worker-contract: $1"; }
fail() { echo "FAIL microagent-e2e-host-worker-contract: $1" >&2; exit 1; }

case "${MICROAGENT_E2E_HOST_WORKER_CONTRACT:-0}" in
  1|true|TRUE|yes|YES|required)
    ;;
  *)
    skip "set MICROAGENT_E2E_HOST_WORKER_CONTRACT=1 to run the opt-in host-worker contract scenario"
    ;;
esac
if [ -z "$HOST_WORKER_URL" ]; then
  skip "set MICROAGENT_E2E_HOST_WORKER_URL or MICROAGENT_HOST_WORKER_URL"
fi

mkdir -p "$(dirname "$REPORT")"
python3 "$ROOT/scripts/dev/microagent-host-worker-conformance.py" \
  --base-url "$HOST_WORKER_URL" \
  --request-model "$REQUEST_MODEL" \
  --runner-engine "$RUNNER_ENGINE" \
  --runner-version "$RUNNER_VERSION" \
  --telemetry-endpoints "$TELEMETRY_ENDPOINTS" \
  --report "$REPORT" >/dev/null || fail "host worker conformance failed"

python3 - "$REPORT" <<'PY'
import json
import sys
from pathlib import Path

doc = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
cap = doc["capabilities"]
sources = ",".join(cap.get("runner_telemetry_sources") or []) or "none"
print(
    "microagent-e2e-host-worker-contract: "
    f"required_ok={cap.get('required_ok')} "
    f"models={cap.get('model_count')} "
    f"streaming={cap.get('streaming_chat_completions')} "
    f"runner_engine={doc.get('runner_engine') or 'unknown'} "
    f"runner_telemetry={sources}"
)
PY

echo "PASS microagent-e2e-host-worker-contract: report written to $REPORT"
