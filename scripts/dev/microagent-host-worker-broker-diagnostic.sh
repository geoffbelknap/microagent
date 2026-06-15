#!/usr/bin/env bash
#
# microagent-host-worker-broker-diagnostic.sh - isolate direct vs broker curl timing.
#
# This diagnostic explains broker overhead before policy mediation exists. It
# uses one running OpenAI-compatible host worker, one measurement-only local
# broker, and one microVM with two guest TCP listeners:
#   - guest-direct -> host worker
#   - guest-broker -> local broker -> host worker
#
# The same payload files are used by host curl and guest curl via
# --data-binary, so request bytes, payload hash, and payload order are stable
# across host-direct, host-broker, guest-direct, and guest-broker lanes.
#
# Required:
#   MICROAGENT_HOST_WORKER_URL                         OpenAI-compatible worker base URL
#
# Optional:
#   MICROAGENT_CLI                                     microagent CLI (default: .build/dev/microagent)
#   MICROAGENT_FIRECRACKER                             Firecracker binary for Linux runs
#   MICROAGENT_HOST_WORKER_DIAGNOSTIC_OUT_DIR          output dir (default: /tmp/microagent-host-worker-broker-diagnostic-$$)
#   MICROAGENT_HOST_WORKER_DIAGNOSTIC_WORKSPACE        workspace name
#   MICROAGENT_HOST_WORKER_DIAGNOSTIC_IMAGE            guest image with curl
#   MICROAGENT_HOST_WORKER_DIAGNOSTIC_STATE_DIR        state dir (default: ~/.microagent)
#   MICROAGENT_HOST_WORKER_DIAGNOSTIC_REPEATS          repeat count for all four lanes (default: 1)
#   MICROAGENT_HOST_WORKER_DIAGNOSTIC_SAMPLES          measured samples per lane/endpoint (default: 3)
#   MICROAGENT_HOST_WORKER_DIAGNOSTIC_WARMUPS          warmups per lane/endpoint (default: 1)
#   MICROAGENT_HOST_WORKER_DIAGNOSTIC_CHAT_TOKENS      tiny chat max tokens (default: 16)
#   MICROAGENT_HOST_WORKER_DIAGNOSTIC_STREAM_TOKENS    stream max tokens (default: 32)
#   MICROAGENT_HOST_WORKER_DIAGNOSTIC_CURL_TIMEOUT     per-request curl timeout seconds (default: 180)
#   MICROAGENT_HOST_WORKER_DIAGNOSTIC_KEEP_FAILED      preserve failed workspace: 0/1 (default: 0)
#   MICROAGENT_HOST_WORKER_MODEL                       request model id override
#   MICROAGENT_HOST_WORKER_MEDIATION_BROKER_HOST       broker bind host (default: 127.0.0.1)
#   MICROAGENT_HOST_WORKER_MEDIATION_BROKER_PORT       broker bind port, 0 means auto (default: 0)
#   MICROAGENT_HOST_WORKER_MEDIATION_BROKER_TIMEOUT    upstream timeout seconds (default: 180)
#   MICROAGENT_HOST_WORKER_MEDIATION_MODE              passthrough|local-allow|policy (default: passthrough)
#   MICROAGENT_HOST_WORKER_MEDIATION_POLICY_URL        policy endpoint for policy mode
#   MICROAGENT_HOST_WORKER_MEDIATION_POLICY_TIMEOUT    policy timeout seconds (default: 2)
#   MICROAGENT_HOST_WORKER_MEDIATION_POLICY_STUB       off|allow|deny|unavailable (default: off)
#   MICROAGENT_HOST_WORKER_DIAGNOSTIC_EXPECT_BROKER_DENIAL  expect brokered lanes to fail closed: 0/1/auto
#   MICROAGENT_HOST_WORKER_MEDIATION_BUDGET            off|report|check (default: report)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

CLI="${MICROAGENT_CLI:-$(e2e_exe "$ROOT/.build/dev/microagent")}"
HOST_WORKER_URL="${MICROAGENT_HOST_WORKER_URL:-}"
REQUEST_MODEL="${MICROAGENT_HOST_WORKER_MODEL:-${MICROAGENT_HOST_WORKER_PROBE_REQUEST_MODEL:-}}"
OUT_DIR="${MICROAGENT_HOST_WORKER_DIAGNOSTIC_OUT_DIR:-/tmp/microagent-host-worker-broker-diagnostic-$$}"
WORKSPACE="${MICROAGENT_HOST_WORKER_DIAGNOSTIC_WORKSPACE:-host-worker-broker-diagnostic-$$}"
IMAGE="${MICROAGENT_HOST_WORKER_DIAGNOSTIC_IMAGE:-docker.io/curlimages/curl:latest}"
STATE_DIR="${MICROAGENT_HOST_WORKER_DIAGNOSTIC_STATE_DIR:-${MICROAGENT_HOST_WORKER_PROBE_STATE_DIR:-$HOME/.microagent}}"
REPEATS="${MICROAGENT_HOST_WORKER_DIAGNOSTIC_REPEATS:-1}"
SAMPLES="${MICROAGENT_HOST_WORKER_DIAGNOSTIC_SAMPLES:-3}"
WARMUPS="${MICROAGENT_HOST_WORKER_DIAGNOSTIC_WARMUPS:-1}"
CHAT_TOKENS="${MICROAGENT_HOST_WORKER_DIAGNOSTIC_CHAT_TOKENS:-16}"
STREAM_TOKENS="${MICROAGENT_HOST_WORKER_DIAGNOSTIC_STREAM_TOKENS:-32}"
CURL_TIMEOUT="${MICROAGENT_HOST_WORKER_DIAGNOSTIC_CURL_TIMEOUT:-180}"
KEEP_FAILED="${MICROAGENT_HOST_WORKER_DIAGNOSTIC_KEEP_FAILED:-0}"
BROKER_HOST="${MICROAGENT_HOST_WORKER_MEDIATION_BROKER_HOST:-127.0.0.1}"
BROKER_PORT="${MICROAGENT_HOST_WORKER_MEDIATION_BROKER_PORT:-0}"
BROKER_TIMEOUT="${MICROAGENT_HOST_WORKER_MEDIATION_BROKER_TIMEOUT:-180}"
MEDIATION_MODE="${MICROAGENT_HOST_WORKER_MEDIATION_MODE:-passthrough}"
MEDIATION_CAPABILITY="${MICROAGENT_HOST_WORKER_MEDIATION_CAPABILITY:-model.openai}"
POLICY_URL="${MICROAGENT_HOST_WORKER_MEDIATION_POLICY_URL:-}"
POLICY_TIMEOUT="${MICROAGENT_HOST_WORKER_MEDIATION_POLICY_TIMEOUT:-2}"
POLICY_STUB="${MICROAGENT_HOST_WORKER_MEDIATION_POLICY_STUB:-off}"
POLICY_STUB_PORT="${MICROAGENT_HOST_WORKER_MEDIATION_POLICY_STUB_PORT:-0}"
POLICY_STUB_DELAY_MS="${MICROAGENT_HOST_WORKER_MEDIATION_POLICY_STUB_DELAY_MS:-0}"
EXPECT_BROKER_DENIAL="${MICROAGENT_HOST_WORKER_DIAGNOSTIC_EXPECT_BROKER_DENIAL:-auto}"
BUDGET_MODE="${MICROAGENT_HOST_WORKER_MEDIATION_BUDGET:-report}"
BUDGET_MODELS_P95_MS="${MICROAGENT_HOST_WORKER_MEDIATION_BUDGET_MODELS_P95_MS:-15}"
BUDGET_CHAT_P95_MS="${MICROAGENT_HOST_WORKER_MEDIATION_BUDGET_CHAT_P95_MS:-50}"
BUDGET_STREAM_TTFB_P95_MS="${MICROAGENT_HOST_WORKER_MEDIATION_BUDGET_STREAM_TTFB_P95_MS:-75}"
BUDGET_REQUEST_BODY_READ_P95_MS="${MICROAGENT_HOST_WORKER_MEDIATION_BUDGET_REQUEST_BODY_READ_P95_MS:-5}"
BUDGET_DECISION_P95_MS="${MICROAGENT_HOST_WORKER_MEDIATION_BUDGET_DECISION_P95_MS:-25}"
BUDGET_UPSTREAM_REQUEST_WRITE_P95_MS="${MICROAGENT_HOST_WORKER_MEDIATION_BUDGET_UPSTREAM_REQUEST_WRITE_P95_MS:-5}"
BROKER_LOG=""
BROKER_STDOUT=""
BROKER_STDERR=""
BROKER_PID=""
POLICY_LOG=""
POLICY_STDOUT=""
POLICY_STDERR=""
POLICY_PID=""
DIRECT_GUEST_PORT=11434
BROKER_GUEST_PORT=11435
DIRECT_VSOCK_PORT=62100
BROKER_VSOCK_PORT=62101
CREATE_FLAGS=()
START_FLAGS=()
CTRL_FLAGS=()

skip() { e2e_skip "microagent-host-worker-broker-diagnostic: $1"; }
fail() { echo "FAIL microagent-host-worker-broker-diagnostic: $1" >&2; exit 1; }

cleanup() {
  local status=$?
  set +e
  if [ -n "$BROKER_PID" ]; then
    kill "$BROKER_PID" >/dev/null 2>&1 || true
    wait "$BROKER_PID" >/dev/null 2>&1 || true
    BROKER_PID=""
  fi
  if [ -n "$POLICY_PID" ]; then
    kill "$POLICY_PID" >/dev/null 2>&1 || true
    wait "$POLICY_PID" >/dev/null 2>&1 || true
    POLICY_PID=""
  fi
  if [ "$status" -ne 0 ] && [ "$KEEP_FAILED" -eq 1 ]; then
    echo "microagent-host-worker-broker-diagnostic: preserving failed workspace $WORKSPACE" >&2
  else
    "$CLI" kill "$WORKSPACE" --state-dir "$STATE_DIR" "${CTRL_FLAGS[@]}" >/dev/null 2>&1 || true
    "$CLI" delete "$WORKSPACE" --force --yes --state-dir "$STATE_DIR" "${CTRL_FLAGS[@]}" >/dev/null 2>&1 || true
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

normalize_worker_url() {
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
    raise SystemExit("worker URL must use http:// for the current microagent bridge")
if parsed.username or parsed.password:
    raise SystemExit("worker URL must not include credentials")
if parsed.query or parsed.fragment or parsed.params:
    raise SystemExit("worker URL must not include query, fragment, or path parameters")
if not parsed.hostname:
    raise SystemExit("worker URL must include a host")
try:
    port = parsed.port or 80
except ValueError as err:
    raise SystemExit(f"invalid worker URL port: {err}") from err

path = parsed.path.rstrip("/") or "/v1"
target_host = parsed.hostname
if ":" in target_host and not target_host.startswith("["):
    target_host = f"[{target_host}]"
base_url = urllib.parse.urlunparse((parsed.scheme, parsed.netloc, path, "", "", ""))
print(json.dumps(
    {
        "base_path": path,
        "base_url": base_url,
        "host": parsed.hostname,
        "port": port,
        "scheme": parsed.scheme,
        "target": f"{target_host}:{port}",
    },
    separators=(",", ":"),
    sort_keys=True,
))
PY
}

choose_port() {
  local host="$1"
  python3 - "$host" <<'PY'
import socket
import sys

host = sys.argv[1]
if host in {"", "0.0.0.0"}:
    host = "127.0.0.1"
with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
    sock.bind((host, 0))
    print(sock.getsockname()[1])
PY
}

broker_connect_host() {
  case "$BROKER_HOST" in
    ''|0.0.0.0)
      printf '%s\n' 127.0.0.1
      ;;
    *)
      printf '%s\n' "$BROKER_HOST"
      ;;
  esac
}

host_worker_health_check() {
  local base_url="$1"
  python3 - "$base_url" <<'PY'
import sys
import urllib.error
import urllib.request

base_url = sys.argv[1].rstrip("/")
health_url = f"{base_url}/models"
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

write_payloads() {
  local payload_dir="$1"
  local model="$2"
  python3 - "$payload_dir" "$model" "$CHAT_TOKENS" "$STREAM_TOKENS" <<'PY'
import hashlib
import json
import sys
from pathlib import Path

payload_dir = Path(sys.argv[1])
model = sys.argv[2] or None
chat_tokens = int(sys.argv[3])
stream_tokens = int(sys.argv[4])
payload_dir.mkdir(parents=True, exist_ok=True)

def write_payload(name, doc):
    if model:
        doc["model"] = model
    body = json.dumps(doc, separators=(",", ":"), sort_keys=False).encode("utf-8")
    path = payload_dir / f"{name}.json"
    path.write_bytes(body)
    return {
        "file": str(path),
        "bytes": len(body),
        "sha256": hashlib.sha256(body).hexdigest(),
    }

manifest = {
    "models": {
        "bytes": 0,
        "sha256": hashlib.sha256(b"").hexdigest(),
    },
    "chat": write_payload(
        "chat",
        {
            "messages": [{"role": "user", "content": "Reply with exactly: PONG"}],
            "max_tokens": chat_tokens,
            "temperature": 0,
        },
    ),
    "stream": write_payload(
        "stream",
        {
            "messages": [
                {
                    "role": "user",
                    "content": "Write one compact paragraph about mediated host GPU workers.",
                }
            ],
            "max_tokens": stream_tokens,
            "temperature": 0,
            "stream": True,
        },
    ),
}
(payload_dir / "manifest.json").write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
print(json.dumps(manifest, sort_keys=True))
PY
}

start_broker() {
  local target_base_url="$1"
  local broker_args=(
    "$ROOT/scripts/dev/microagent-host-worker-broker.py"
    --target-base-url "$target_base_url"
    --bind-host "$BROKER_HOST"
    --bind-port "$BROKER_PORT"
    --log-path "$BROKER_LOG"
    --timeout "$BROKER_TIMEOUT"
    --mediation-mode "$MEDIATION_MODE"
    --workspace-id "$WORKSPACE"
    --capability "$MEDIATION_CAPABILITY"
  )
  if [ -n "$POLICY_URL" ]; then
    broker_args+=(--policy-url "$POLICY_URL" --policy-timeout "$POLICY_TIMEOUT")
  fi
  python3 "${broker_args[@]}" >"$BROKER_STDOUT" 2>"$BROKER_STDERR" &
  BROKER_PID="$!"
}

start_policy_stub() {
  local decision="$1"
  python3 "$ROOT/scripts/dev/microagent-host-worker-policy-stub.py" \
    --bind-host "$BROKER_HOST" \
    --bind-port "$POLICY_STUB_PORT" \
    --decision "$decision" \
    --delay-ms "$POLICY_STUB_DELAY_MS" \
    --log-path "$POLICY_LOG" >"$POLICY_STDOUT" 2>"$POLICY_STDERR" &
  POLICY_PID="$!"
}

wait_for_broker() {
  local health_url="$1"
  local deadline=$((SECONDS + 20))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if python3 - "$health_url" <<'PY' >/dev/null 2>&1
import sys
import urllib.request

with urllib.request.urlopen(sys.argv[1], timeout=1) as response:
    if 200 <= response.status < 300:
        raise SystemExit(0)
raise SystemExit(1)
PY
    then
      return 0
    fi
    if ! kill -0 "$BROKER_PID" >/dev/null 2>&1; then
      [ ! -s "$BROKER_STDERR" ] || sed -n '1,120p' "$BROKER_STDERR" >&2
      fail "broker exited before becoming ready"
    fi
    sleep 0.2
  done
  [ ! -s "$BROKER_STDERR" ] || sed -n '1,120p' "$BROKER_STDERR" >&2
  fail "broker did not become ready at $health_url"
}

wait_for_policy_stub() {
  local health_url="$1"
  local deadline=$((SECONDS + 20))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if python3 - "$health_url" <<'PY' >/dev/null 2>&1
import sys
import urllib.request

with urllib.request.urlopen(sys.argv[1], timeout=1) as response:
    if 200 <= response.status < 300:
        raise SystemExit(0)
raise SystemExit(1)
PY
    then
      return 0
    fi
    if ! kill -0 "$POLICY_PID" >/dev/null 2>&1; then
      [ ! -s "$POLICY_STDERR" ] || sed -n '1,120p' "$POLICY_STDERR" >&2
      fail "policy stub exited before becoming ready"
    fi
    sleep 0.2
  done
  [ ! -s "$POLICY_STDERR" ] || sed -n '1,120p' "$POLICY_STDERR" >&2
  fail "policy stub did not become ready at $health_url"
}

read -r -d '' MEASURE_SH <<'SH' || true
set -eu

measure_endpoint() {
  endpoint="$1"
  payload_file=""
  case "$endpoint" in
    models)
      method="GET"
      path="models"
      ;;
    chat)
      method="POST"
      path="chat/completions"
      payload_file="$MEASURE_PAYLOAD_DIR/chat.json"
      ;;
    stream)
      method="POST"
      path="chat/completions"
      payload_file="$MEASURE_PAYLOAD_DIR/stream.json"
      ;;
    *)
      echo "unknown endpoint: $endpoint" >&2
      exit 1
      ;;
  esac
  url="$MEASURE_BASE_URL/$path"

  total=$((MEASURE_SAMPLES + MEASURE_WARMUPS))
  i=0
  while [ "$i" -lt "$total" ]; do
    body="$(mktemp)"
    if [ "$method" = "GET" ]; then
      metrics="$(curl -sS --max-time "$MEASURE_CURL_TIMEOUT" -o "$body" -w '%{time_connect} %{time_pretransfer} %{time_starttransfer} %{time_total} %{size_download} %{http_code}' "$url")"
      request_bytes=0
    else
      metrics="$(curl -sS -N --max-time "$MEASURE_CURL_TIMEOUT" -o "$body" -w '%{time_connect} %{time_pretransfer} %{time_starttransfer} %{time_total} %{size_download} %{http_code}' "$url" -H "Content-Type: application/json" --data-binary "@$payload_file")"
      request_bytes="$(wc -c <"$payload_file" | tr -d ' ')"
    fi
    set -- $metrics
    connect="$1"
    pretransfer="$2"
    ttfb="$3"
    total_time="$4"
    response_bytes="$5"
    status="$6"
    chunks=0
    if [ "${MEASURE_EXPECT_DENIED:-0}" = "1" ]; then
      case "$status" in
        403|503)
          ;;
        *)
          echo "expected broker denial status 403/503 for $endpoint, got $status" >&2
          cat "$body" >&2
          rm -f "$body"
          exit 1
          ;;
      esac
    else
      case "$status" in
        2*)
          ;;
        *)
          echo "$endpoint returned unexpected HTTP $status" >&2
          cat "$body" >&2
          rm -f "$body"
          exit 1
          ;;
      esac
      case "$endpoint" in
        models)
          grep -Eq '"object"|"data"' "$body" || { echo "models response did not look OpenAI-compatible" >&2; cat "$body" >&2; rm -f "$body"; exit 1; }
          ;;
        chat)
          grep -q '"choices"' "$body" || { echo "chat response did not contain choices" >&2; cat "$body" >&2; rm -f "$body"; exit 1; }
          ;;
        stream)
          grep -Eq '\[DONE\]|"choices"' "$body" || { echo "stream response did not look OpenAI-compatible" >&2; cat "$body" >&2; rm -f "$body"; exit 1; }
          chunks="$(grep -c '^data:' "$body" 2>/dev/null || true)"
          chunks="${chunks:-0}"
          ;;
      esac
    fi
    if [ "$i" -ge "$MEASURE_WARMUPS" ]; then
      sample_index=$((i - MEASURE_WARMUPS))
      printf '{"lane":"%s","endpoint":"%s","repeat_index":%s,"sample_index":%s,"connect_seconds":%s,"pretransfer_seconds":%s,"ttfb_seconds":%s,"total_seconds":%s,"response_bytes":%s,"status":%s,"chunks":%s,"request_bytes":%s}\n' \
        "$MEASURE_LANE" "$endpoint" "${MEASURE_REPEAT_INDEX:-0}" "$sample_index" "$connect" "$pretransfer" "$ttfb" "$total_time" "$response_bytes" "$status" "$chunks" "$request_bytes"
    fi
    rm -f "$body"
    i=$((i + 1))
  done
}

measure_endpoint models
measure_endpoint chat
measure_endpoint stream
SH

run_host_lane() {
  local lane="$1"
  local base_url="$2"
  local out="$3"
  local repeat_index="$4"
  local expect_denied=0
  if [ "$EXPECT_BROKER_DENIAL" -eq 1 ]; then
    case "$lane" in
      *-broker)
        expect_denied=1
        ;;
    esac
  fi
  MEASURE_LANE="$lane" \
    MEASURE_REPEAT_INDEX="$repeat_index" \
    MEASURE_BASE_URL="$base_url" \
    MEASURE_PAYLOAD_DIR="$OUT_DIR/payloads" \
    MEASURE_SAMPLES="$SAMPLES" \
    MEASURE_WARMUPS="$WARMUPS" \
    MEASURE_CURL_TIMEOUT="$CURL_TIMEOUT" \
    MEASURE_EXPECT_DENIED="$expect_denied" \
    sh -c "$MEASURE_SH" >"$out"
}

run_guest_lane() {
  local lane="$1"
  local base_url="$2"
  local out="$3"
  local repeat_index="$4"
  local expect_denied=0
  if [ "$EXPECT_BROKER_DENIAL" -eq 1 ]; then
    case "$lane" in
      *-broker)
        expect_denied=1
        ;;
    esac
  fi
  "$CLI" exec "$WORKSPACE" --state-dir "$STATE_DIR" --timeout "$CURL_TIMEOUT"s \
    -env "MEASURE_LANE=$lane" \
    -env "MEASURE_REPEAT_INDEX=$repeat_index" \
    -env "MEASURE_BASE_URL=$base_url" \
    -env "MEASURE_PAYLOAD_DIR=/tmp/microagent-host-worker-diagnostic" \
    -env "MEASURE_SAMPLES=$SAMPLES" \
    -env "MEASURE_WARMUPS=$WARMUPS" \
    -env "MEASURE_CURL_TIMEOUT=$CURL_TIMEOUT" \
    -env "MEASURE_EXPECT_DENIED=$expect_denied" \
    -- sh -c "$MEASURE_SH" >"$out"
}

write_summary() {
  local raw_jsonl="$1"
  local payload_manifest="$2"
  local summary_tsv="$3"
  local comparison_tsv="$4"
  python3 - "$raw_jsonl" "$payload_manifest" "$summary_tsv" "$comparison_tsv" <<'PY'
import csv
import json
import math
import statistics
import sys
from pathlib import Path

raw_path = Path(sys.argv[1])
payload_manifest = json.loads(Path(sys.argv[2]).read_text(encoding="utf-8"))
summary_path = Path(sys.argv[3])
comparison_path = Path(sys.argv[4])
rows = [json.loads(line) for line in raw_path.read_text(encoding="utf-8").splitlines() if line.strip()]
lanes = ("host-direct", "host-broker", "guest-direct", "guest-broker")
endpoints = ("models", "chat", "stream")

def clean_numbers(values):
    clean = sorted(float(value) for value in values)
    return clean

def percentile(values, pct):
    clean = clean_numbers(values)
    if not clean:
        return None
    index = max(0, min(len(clean) - 1, math.ceil((pct / 100.0) * len(clean)) - 1))
    return round(clean[index], 3)

def stats(values):
    clean = clean_numbers(values)
    if not clean:
        return {
            "median": None,
            "p95": None,
            "min": None,
            "max": None,
        }
    return {
        "median": round(statistics.median(clean), 3),
        "p95": percentile(clean, 95),
        "min": round(clean[0], 3),
        "max": round(clean[-1], 3),
    }

def ms(row, key):
    return float(row[key]) * 1000

def body_ms(row):
    return max(0.0, ms(row, "total_seconds") - ms(row, "ttfb_seconds"))

def metric_ms(row, metric):
    if metric == "total":
        return ms(row, "total_seconds")
    if metric == "ttfb":
        return ms(row, "ttfb_seconds")
    if metric == "body":
        return body_ms(row)
    raise ValueError(metric)

def repeat_count(samples):
    return len({str(row.get("repeat_index", 0)) for row in samples})

def summarize(lane, endpoint):
    samples = [row for row in rows if row.get("lane") == lane and row.get("endpoint") == endpoint]
    if not samples:
        return None
    payload = payload_manifest.get(endpoint) or {}
    total = stats(ms(row, "total_seconds") for row in samples)
    ttfb = stats(ms(row, "ttfb_seconds") for row in samples)
    connect = stats(ms(row, "connect_seconds") for row in samples)
    pretransfer = stats(ms(row, "pretransfer_seconds") for row in samples)
    body = stats(body_ms(row) for row in samples)
    return {
        "lane": lane,
        "endpoint": endpoint,
        "repeat_count": repeat_count(samples),
        "sample_count": len(samples),
        "request_bytes": samples[0].get("request_bytes"),
        "payload_bytes": payload.get("bytes"),
        "payload_sha256": payload.get("sha256"),
        "status_codes": ",".join(sorted({str(row.get("status")) for row in samples})),
        "median_ms": total["median"],
        "p95_ms": total["p95"],
        "min_ms": total["min"],
        "max_ms": total["max"],
        "ttfb_median_ms": ttfb["median"],
        "ttfb_p95_ms": ttfb["p95"],
        "ttfb_min_ms": ttfb["min"],
        "ttfb_max_ms": ttfb["max"],
        "connect_median_ms": connect["median"],
        "connect_p95_ms": connect["p95"],
        "pretransfer_median_ms": pretransfer["median"],
        "pretransfer_p95_ms": pretransfer["p95"],
        "body_read_median_ms": body["median"],
        "body_read_p95_ms": body["p95"],
        "body_read_min_ms": body["min"],
        "body_read_max_ms": body["max"],
        "response_bytes_median": stats(row.get("response_bytes") for row in samples)["median"],
        "chunks_median": stats(row.get("chunks") for row in samples)["median"],
    }

summary_rows = []
for lane in lanes:
    for endpoint in endpoints:
        item = summarize(lane, endpoint)
        if item:
            summary_rows.append(item)

summary_fields = (
    "lane",
    "endpoint",
    "repeat_count",
    "sample_count",
    "request_bytes",
    "payload_bytes",
    "payload_sha256",
    "status_codes",
    "median_ms",
    "p95_ms",
    "min_ms",
    "max_ms",
    "ttfb_median_ms",
    "ttfb_p95_ms",
    "ttfb_min_ms",
    "ttfb_max_ms",
    "connect_median_ms",
    "connect_p95_ms",
    "pretransfer_median_ms",
    "pretransfer_p95_ms",
    "body_read_median_ms",
    "body_read_p95_ms",
    "body_read_min_ms",
    "body_read_max_ms",
    "response_bytes_median",
    "chunks_median",
)
with summary_path.open("w", encoding="utf-8", newline="") as f:
    writer = csv.DictWriter(f, fieldnames=summary_fields, delimiter="\t", lineterminator="\n")
    writer.writeheader()
    writer.writerows(summary_rows)

def sample_key(row):
    return (str(row.get("repeat_index", 0)), str(row.get("sample_index", 0)))

def paired_diffs(endpoint, left_lane, right_lane, metric):
    left_samples = {
        sample_key(row): row
        for row in rows
        if row.get("lane") == left_lane and row.get("endpoint") == endpoint
    }
    right_samples = {
        sample_key(row): row
        for row in rows
        if row.get("lane") == right_lane and row.get("endpoint") == endpoint
    }
    values = []
    for key in sorted(set(left_samples) & set(right_samples)):
        values.append(
            metric_ms(right_samples[key], metric) - metric_ms(left_samples[key], metric)
        )
    return values

def add_diff(row, endpoint, field, left_lane, right_lane, metric):
    values = paired_diffs(endpoint, left_lane, right_lane, metric)
    item = stats(values)
    row[field] = item["median"]
    row[field.replace("_ms", "_p95_ms")] = item["p95"]
    row[field.replace("_ms", "_min_ms")] = item["min"]
    row[field.replace("_ms", "_max_ms")] = item["max"]
    return len(values)

def endpoint_repeat_count(endpoint):
    return len(
        {
            str(row.get("repeat_index", 0))
            for row in rows
            if row.get("endpoint") == endpoint
        }
    )

diff_specs = (
    ("host_broker_overhead_ms", "host-direct", "host-broker", "total"),
    ("guest_broker_overhead_ms", "guest-direct", "guest-broker", "total"),
    ("direct_bridge_overhead_ms", "host-direct", "guest-direct", "total"),
    ("broker_bridge_overhead_ms", "host-broker", "guest-broker", "total"),
    ("host_broker_ttfb_overhead_ms", "host-direct", "host-broker", "ttfb"),
    ("guest_broker_ttfb_overhead_ms", "guest-direct", "guest-broker", "ttfb"),
    ("direct_bridge_ttfb_overhead_ms", "host-direct", "guest-direct", "ttfb"),
    ("broker_bridge_ttfb_overhead_ms", "host-broker", "guest-broker", "ttfb"),
    ("host_broker_body_overhead_ms", "host-direct", "host-broker", "body"),
    ("guest_broker_body_overhead_ms", "guest-direct", "guest-broker", "body"),
)

comparison_rows = []
for endpoint in endpoints:
    item = {
        "endpoint": endpoint,
        "repeat_count": endpoint_repeat_count(endpoint),
        "paired_sample_count": None,
    }
    paired_counts = []
    for field, left_lane, right_lane, metric in diff_specs:
        paired_counts.append(add_diff(item, endpoint, field, left_lane, right_lane, metric))
    item["paired_sample_count"] = min(paired_counts) if paired_counts else 0
    comparison_rows.append(item)

comparison_fields = ["endpoint", "repeat_count", "paired_sample_count"]
for field, *_ in diff_specs:
    comparison_fields.extend(
        [
            field,
            field.replace("_ms", "_p95_ms"),
            field.replace("_ms", "_min_ms"),
            field.replace("_ms", "_max_ms"),
        ]
    )
with comparison_path.open("w", encoding="utf-8", newline="") as f:
    writer = csv.DictWriter(f, fieldnames=comparison_fields, delimiter="\t", lineterminator="\n")
    writer.writeheader()
    writer.writerows(comparison_rows)
PY
}

write_comparison_compact() {
  local comparison_tsv="$1"
  local compact_tsv="$2"
  python3 - "$comparison_tsv" "$compact_tsv" <<'PY'
import csv
import sys
from pathlib import Path

comparison_path = Path(sys.argv[1])
compact_path = Path(sys.argv[2])
fields = (
    "endpoint",
    "repeat_count",
    "paired_sample_count",
    "guest_broker_overhead_ms",
    "guest_broker_overhead_p95_ms",
    "guest_broker_ttfb_overhead_ms",
    "guest_broker_ttfb_overhead_p95_ms",
    "guest_broker_body_overhead_ms",
    "guest_broker_body_overhead_p95_ms",
    "host_broker_overhead_ms",
    "host_broker_overhead_p95_ms",
    "host_broker_ttfb_overhead_ms",
    "host_broker_ttfb_overhead_p95_ms",
    "direct_bridge_overhead_ms",
    "direct_bridge_overhead_p95_ms",
    "broker_bridge_overhead_ms",
    "broker_bridge_overhead_p95_ms",
)
with comparison_path.open(encoding="utf-8", newline="") as f:
    rows = list(csv.DictReader(f, delimiter="\t"))
with compact_path.open("w", encoding="utf-8", newline="") as f:
    writer = csv.DictWriter(
        f,
        fieldnames=fields,
        delimiter="\t",
        extrasaction="ignore",
        lineterminator="\n",
    )
    writer.writeheader()
    writer.writerows(rows)
PY
}

write_broker_summary() {
  local log_path="$1"
  local json_path="$2"
  local tsv_path="$3"
  python3 - "$log_path" "$json_path" "$tsv_path" <<'PY'
import csv
import math
import json
import sys
from collections import Counter, defaultdict
from pathlib import Path

log_path = Path(sys.argv[1])
json_path = Path(sys.argv[2])
tsv_path = Path(sys.argv[3])
events = []
if log_path.exists():
    for line in log_path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            continue
ends = [event for event in events if event.get("event") == "request_end"]
errors = [event for event in events if event.get("event") == "request_error"]
phase_fields = (
    "duration_ms",
    "request_body_read_ms",
    "mediation_decision_ms",
    "upstream_request_write_ms",
    "upstream_ttfb_ms",
    "upstream_first_body_byte_ms",
    "downstream_first_body_byte_ms",
    "response_body_ms",
    "downstream_complete_ms",
)
paths = defaultdict(
    lambda: {
        "request_count": 0,
        "error_count": 0,
        "response_bytes": 0,
        "request_bytes": Counter(),
        "status_counts": Counter(),
        "mediation_results": Counter(),
        "phases": {field: [] for field in phase_fields},
    }
)
for event in ends:
    item = paths[event.get("path") or ""]
    item["request_count"] += 1
    item["response_bytes"] += int(event.get("response_bytes") or 0)
    item["request_bytes"].update([str(event.get("request_bytes") or 0)])
    if event.get("status") is not None:
        item["status_counts"].update([str(event["status"])])
    if event.get("mediation_result") is not None:
        item["mediation_results"].update([str(event["mediation_result"])])
    for field in phase_fields:
        if isinstance(event.get(field), (int, float)):
            item["phases"][field].append(float(event[field]))
for event in errors:
    item = paths[event.get("path") or ""]
    item["error_count"] += 1
    for field in phase_fields:
        if isinstance(event.get(field), (int, float)):
            item["phases"][field].append(float(event[field]))

def percentile(values, pct):
    if not values:
        return None
    ordered = sorted(values)
    index = max(0, min(len(ordered) - 1, math.ceil((pct / 100.0) * len(ordered)) - 1))
    return round(ordered[index], 3)

def median(values):
    if not values:
        return None
    ordered = sorted(values)
    mid = len(ordered) // 2
    if len(ordered) % 2:
        return round(ordered[mid], 3)
    return round((ordered[mid - 1] + ordered[mid]) / 2, 3)

rows = []
for path, item in sorted(paths.items()):
    row = {
        "path": path,
        "request_count": item["request_count"],
        "error_count": item["error_count"],
        "response_bytes": item["response_bytes"],
        "request_bytes_values": ",".join(sorted(item["request_bytes"])),
        "status_counts": ",".join(
            f"{status}:{count}"
            for status, count in sorted(item["status_counts"].items())
        ),
        "mediation_results": ",".join(
            f"{result}:{count}"
            for result, count in sorted(item["mediation_results"].items())
        ),
    }
    for field in phase_fields:
        prefix = field.removesuffix("_ms")
        row[f"{prefix}_median_ms"] = median(item["phases"][field])
        row[f"{prefix}_p95_ms"] = percentile(item["phases"][field], 95)
    rows.append(row)
summary = {
    "event_count": len(events),
    "request_count": len(ends),
    "error_count": len(errors),
    "status_counts": dict(Counter(str(event.get("status")) for event in ends if event.get("status") is not None)),
    "paths": rows,
}
json_path.write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
with tsv_path.open("w", encoding="utf-8", newline="") as f:
    fields = (
        "path",
        "request_count",
        "error_count",
        "response_bytes",
        "request_bytes_values",
        "status_counts",
        "mediation_results",
    )
    phase_columns = []
    for field in phase_fields:
        prefix = field.removesuffix("_ms")
        phase_columns.extend((f"{prefix}_median_ms", f"{prefix}_p95_ms"))
    writer = csv.DictWriter(
        f,
        fieldnames=fields + tuple(phase_columns),
        delimiter="\t",
        lineterminator="\n",
    )
    writer.writeheader()
    writer.writerows(rows)
PY
}

for value_name in REPEATS SAMPLES WARMUPS CHAT_TOKENS STREAM_TOKENS CURL_TIMEOUT BROKER_PORT; do
  value="${!value_name}"
  case "$value" in
    ''|*[!0-9]*)
      fail "$value_name must be a non-negative integer"
      ;;
  esac
done
if [ "$REPEATS" -le 0 ] || [ "$SAMPLES" -le 0 ]; then
  fail "REPEATS and SAMPLES must be > 0"
fi
case "$KEEP_FAILED" in
  1|true|TRUE|yes|YES)
    KEEP_FAILED=1
    ;;
  0|false|FALSE|no|NO)
    KEEP_FAILED=0
    ;;
  *)
    fail "MICROAGENT_HOST_WORKER_DIAGNOSTIC_KEEP_FAILED must be 0/1, true/false, or yes/no"
    ;;
esac
case "$BROKER_TIMEOUT" in
  ''|*[!0-9.]*)
    fail "MICROAGENT_HOST_WORKER_MEDIATION_BROKER_TIMEOUT must be numeric"
    ;;
esac
case "$POLICY_TIMEOUT" in
  ''|*[!0-9.]*)
    fail "MICROAGENT_HOST_WORKER_MEDIATION_POLICY_TIMEOUT must be numeric"
    ;;
esac
case "$POLICY_STUB_DELAY_MS" in
  ''|*[!0-9.]*)
    fail "MICROAGENT_HOST_WORKER_MEDIATION_POLICY_STUB_DELAY_MS must be numeric"
    ;;
esac
case "$MEDIATION_MODE" in
  passthrough|local-allow|policy)
    ;;
  *)
    fail "MICROAGENT_HOST_WORKER_MEDIATION_MODE must be passthrough, local-allow, or policy"
    ;;
esac
case "$POLICY_STUB" in
  off|allow|deny|unavailable)
    ;;
  *)
    fail "MICROAGENT_HOST_WORKER_MEDIATION_POLICY_STUB must be off, allow, deny, or unavailable"
    ;;
esac
if [ "$MEDIATION_MODE" != "policy" ] && [ "$POLICY_STUB" != "off" ]; then
  fail "MICROAGENT_HOST_WORKER_MEDIATION_POLICY_STUB requires MICROAGENT_HOST_WORKER_MEDIATION_MODE=policy"
fi
case "$EXPECT_BROKER_DENIAL" in
  auto)
    if [ "$MEDIATION_MODE" = "policy" ]; then
      case "$POLICY_STUB" in
        deny|unavailable)
          EXPECT_BROKER_DENIAL=1
          ;;
        *)
          EXPECT_BROKER_DENIAL=0
          ;;
      esac
    else
      EXPECT_BROKER_DENIAL=0
    fi
    ;;
  1|true|TRUE|yes|YES)
    EXPECT_BROKER_DENIAL=1
    ;;
  0|false|FALSE|no|NO)
    EXPECT_BROKER_DENIAL=0
    ;;
  *)
    fail "MICROAGENT_HOST_WORKER_DIAGNOSTIC_EXPECT_BROKER_DENIAL must be auto, 0/1, true/false, or yes/no"
    ;;
esac
case "$BUDGET_MODE" in
  off|report|check)
    ;;
  *)
    fail "MICROAGENT_HOST_WORKER_MEDIATION_BUDGET must be off, report, or check"
    ;;
esac
if [ "$BROKER_PORT" -eq 0 ]; then
  BROKER_PORT="$(choose_port "$BROKER_HOST")"
fi
if [ "$POLICY_STUB_PORT" = "0" ]; then
  POLICY_STUB_PORT="$(choose_port "$BROKER_HOST")"
elif [ -n "${POLICY_STUB_PORT//[0-9]/}" ]; then
  fail "MICROAGENT_HOST_WORKER_MEDIATION_POLICY_STUB_PORT must be a non-negative integer"
fi
if [ "$MEDIATION_MODE" = "policy" ] && [ "$POLICY_STUB" = "off" ] && [ -z "$POLICY_URL" ]; then
  fail "MICROAGENT_HOST_WORKER_MEDIATION_POLICY_URL is required when policy mode does not start a stub"
fi
if [ -z "$HOST_WORKER_URL" ]; then
  fail "MICROAGENT_HOST_WORKER_URL must point at an OpenAI-compatible host worker"
fi
if [ ! -x "$CLI" ]; then
  skip "CLI not found at $CLI (run scripts/dev/build-local.sh)"
fi
case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    skip "broker diagnostic currently targets Linux amd64 Firecracker hosts"
    ;;
esac
if [ ! -e /dev/kvm ]; then
  skip "/dev/kvm not available"
fi
if [ -z "${MICROAGENT_FIRECRACKER:-}" ]; then
  MICROAGENT_FIRECRACKER="$(e2e_resolve_firecracker)" || skip "Firecracker binary not resolved"
  export MICROAGENT_FIRECRACKER
elif [ ! -x "${MICROAGENT_FIRECRACKER:-/nonexistent}" ]; then
  skip "MICROAGENT_FIRECRACKER not executable: $MICROAGENT_FIRECRACKER"
fi
CREATE_FLAGS=(--backend firecracker)
START_FLAGS=(--backend firecracker)
CTRL_FLAGS=(--backend firecracker)

mkdir -p "$OUT_DIR"
BROKER_LOG="$OUT_DIR/broker.jsonl"
BROKER_STDOUT="$OUT_DIR/broker.stdout"
BROKER_STDERR="$OUT_DIR/broker.stderr"
POLICY_LOG="$OUT_DIR/policy-stub.jsonl"
POLICY_STDOUT="$OUT_DIR/policy-stub.stdout"
POLICY_STDERR="$OUT_DIR/policy-stub.stderr"
: >"$BROKER_LOG"
: >"$POLICY_LOG"

WORKER_INFO="$(normalize_worker_url "$HOST_WORKER_URL")" || fail "invalid MICROAGENT_HOST_WORKER_URL"
TARGET_BASE_URL="$(printf '%s' "$WORKER_INFO" | json_get base_url)"
TARGET_BASE_PATH="$(printf '%s' "$WORKER_INFO" | json_get base_path)"
DIRECT_TARGET="$(printf '%s' "$WORKER_INFO" | json_get target)"
BROKER_TARGET="$(broker_connect_host):$BROKER_PORT"
BROKER_URL="http://$(broker_connect_host):$BROKER_PORT$TARGET_BASE_PATH"
BROKER_HEALTH_URL="http://$(broker_connect_host):$BROKER_PORT/healthz"
POLICY_HEALTH_URL="http://$(broker_connect_host):$POLICY_STUB_PORT/healthz"
GUEST_DIRECT_URL="http://127.0.0.1:$DIRECT_GUEST_PORT$TARGET_BASE_PATH"
GUEST_BROKER_URL="http://127.0.0.1:$BROKER_GUEST_PORT$TARGET_BASE_PATH"

case "$POLICY_STUB" in
  allow|deny)
    POLICY_URL="http://$(broker_connect_host):$POLICY_STUB_PORT/decision"
    echo "microagent-host-worker-broker-diagnostic: starting policy stub decision=$POLICY_STUB url=$POLICY_URL"
    start_policy_stub "$POLICY_STUB"
    wait_for_policy_stub "$POLICY_HEALTH_URL"
    ;;
  unavailable)
    POLICY_URL="http://$(broker_connect_host):$POLICY_STUB_PORT/decision"
    echo "microagent-host-worker-broker-diagnostic: using intentionally unavailable policy url=$POLICY_URL"
    ;;
esac

host_worker_health_check "$TARGET_BASE_URL" || fail "external host worker health check failed"
if [ -z "$REQUEST_MODEL" ]; then
  if REQUEST_MODEL="$(discover_request_model "$TARGET_BASE_URL" 2>/dev/null)"; then
    echo "microagent-host-worker-broker-diagnostic: using request model $REQUEST_MODEL"
  else
    echo "microagent-host-worker-broker-diagnostic: no request model discovered; chat payloads will omit model"
    REQUEST_MODEL=""
  fi
else
  echo "microagent-host-worker-broker-diagnostic: using configured request model $REQUEST_MODEL"
fi

PAYLOAD_DIR="$OUT_DIR/payloads"
PAYLOAD_MANIFEST_JSON="$(write_payloads "$PAYLOAD_DIR" "$REQUEST_MODEL")"
printf '%s\n' "$PAYLOAD_MANIFEST_JSON" >"$OUT_DIR/payloads-manifest.compact.json"

echo "microagent-host-worker-broker-diagnostic: starting broker $BROKER_URL -> $TARGET_BASE_URL mode=$MEDIATION_MODE"
start_broker "$TARGET_BASE_URL"
wait_for_broker "$BROKER_HEALTH_URL"

"$CLI" delete "$WORKSPACE" --force --yes --state-dir "$STATE_DIR" "${CTRL_FLAGS[@]}" >/dev/null 2>&1 || true
echo "microagent-host-worker-broker-diagnostic: creating workspace $WORKSPACE"
"$CLI" create "$WORKSPACE" \
  --image "$IMAGE" \
  --state-dir "$STATE_DIR" \
  --mediation "$DIRECT_VSOCK_PORT=$DIRECT_TARGET" \
  --env "MICROAGENT_VSOCK_TCP_LISTENERS=127.0.0.1:$DIRECT_GUEST_PORT=$DIRECT_VSOCK_PORT,127.0.0.1:$BROKER_GUEST_PORT=$BROKER_VSOCK_PORT" \
  --env "MICROAGENT_DIRECT_MODEL_URL=$GUEST_DIRECT_URL" \
  --env "MICROAGENT_BROKER_MODEL_URL=$GUEST_BROKER_URL" \
  "${CREATE_FLAGS[@]}" >/dev/null || fail "workspace create failed"
"$CLI" start "$WORKSPACE" \
  --state-dir "$STATE_DIR" \
  --vsock "$BROKER_VSOCK_PORT=$BROKER_TARGET" \
  "${START_FLAGS[@]}" >/dev/null || fail "workspace start failed"
if ! e2e_wait_exec_ready "$CLI" "$STATE_DIR" "$WORKSPACE" 90; then
  "$CLI" --json status "$WORKSPACE" --state-dir "$STATE_DIR" "${CTRL_FLAGS[@]}" >&2 || true
  fail "workspace exec service did not become ready"
fi

echo "microagent-host-worker-broker-diagnostic: copying byte-identical payloads into guest"
"$CLI" exec "$WORKSPACE" --state-dir "$STATE_DIR" -- sh -c "mkdir -p /tmp/microagent-host-worker-diagnostic" >/dev/null
"$CLI" exec "$WORKSPACE" --state-dir "$STATE_DIR" --stdin "$PAYLOAD_DIR/chat.json" -- sh -c "cat > /tmp/microagent-host-worker-diagnostic/chat.json" >/dev/null
"$CLI" exec "$WORKSPACE" --state-dir "$STATE_DIR" --stdin "$PAYLOAD_DIR/stream.json" -- sh -c "cat > /tmp/microagent-host-worker-diagnostic/stream.json" >/dev/null

rm -rf "$OUT_DIR/repeats"
mkdir -p "$OUT_DIR/repeats"
for lane in host-direct host-broker guest-direct guest-broker; do
  : >"$OUT_DIR/$lane.jsonl"
done

repeat_index=0
while [ "$repeat_index" -lt "$REPEATS" ]; do
  repeat_label="$(printf '%03d' "$repeat_index")"
  repeat_dir="$OUT_DIR/repeats/$repeat_label"
  mkdir -p "$repeat_dir"

  echo "microagent-host-worker-broker-diagnostic: repeat=$repeat_label measuring host-direct"
  run_host_lane host-direct "$TARGET_BASE_URL" "$repeat_dir/host-direct.jsonl" "$repeat_index"
  echo "microagent-host-worker-broker-diagnostic: repeat=$repeat_label measuring host-broker"
  run_host_lane host-broker "$BROKER_URL" "$repeat_dir/host-broker.jsonl" "$repeat_index"
  echo "microagent-host-worker-broker-diagnostic: repeat=$repeat_label measuring guest-direct"
  run_guest_lane guest-direct "$GUEST_DIRECT_URL" "$repeat_dir/guest-direct.jsonl" "$repeat_index"
  echo "microagent-host-worker-broker-diagnostic: repeat=$repeat_label measuring guest-broker"
  run_guest_lane guest-broker "$GUEST_BROKER_URL" "$repeat_dir/guest-broker.jsonl" "$repeat_index"

  for lane in host-direct host-broker guest-direct guest-broker; do
    cat "$repeat_dir/$lane.jsonl" >>"$OUT_DIR/$lane.jsonl"
  done
  repeat_index=$((repeat_index + 1))
done

cat "$OUT_DIR/host-direct.jsonl" \
  "$OUT_DIR/host-broker.jsonl" \
  "$OUT_DIR/guest-direct.jsonl" \
  "$OUT_DIR/guest-broker.jsonl" >"$OUT_DIR/raw.jsonl"
write_summary "$OUT_DIR/raw.jsonl" "$PAYLOAD_DIR/manifest.json" "$OUT_DIR/summary.tsv" "$OUT_DIR/comparison.tsv"
write_comparison_compact "$OUT_DIR/comparison.tsv" "$OUT_DIR/comparison-compact.tsv"
write_broker_summary "$BROKER_LOG" "$OUT_DIR/broker-summary.json" "$OUT_DIR/broker-summary.tsv"
if [ "$BUDGET_MODE" != "off" ] && [ "$EXPECT_BROKER_DENIAL" -eq 0 ]; then
  budget_args=(
    --comparison "$OUT_DIR/comparison.tsv"
    --broker-summary "$OUT_DIR/broker-summary.tsv"
    --output-json "$OUT_DIR/mediation-budget.json"
    --output-tsv "$OUT_DIR/mediation-budget.tsv"
    --models-p95-ms "$BUDGET_MODELS_P95_MS"
    --chat-p95-ms "$BUDGET_CHAT_P95_MS"
    --stream-ttfb-p95-ms "$BUDGET_STREAM_TTFB_P95_MS"
    --broker-request-body-read-p95-ms "$BUDGET_REQUEST_BODY_READ_P95_MS"
    --broker-mediation-decision-p95-ms "$BUDGET_DECISION_P95_MS"
    --broker-upstream-request-write-p95-ms "$BUDGET_UPSTREAM_REQUEST_WRITE_P95_MS"
  )
  if [ "$BUDGET_MODE" = "check" ]; then
    budget_args+=(--check)
  fi
  python3 "$ROOT/scripts/dev/microagent-host-worker-mediation-budget.py" "${budget_args[@]}"
fi

echo "microagent-host-worker-broker-diagnostic: reports written under $OUT_DIR"
cat "$OUT_DIR/comparison-compact.tsv"
if [ -s "$OUT_DIR/mediation-budget.tsv" ]; then
  cat "$OUT_DIR/mediation-budget.tsv"
fi
echo "PASS microagent-host-worker-broker-diagnostic"
