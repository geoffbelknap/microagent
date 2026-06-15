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
SAMPLES="${MICROAGENT_HOST_WORKER_DIAGNOSTIC_SAMPLES:-3}"
WARMUPS="${MICROAGENT_HOST_WORKER_DIAGNOSTIC_WARMUPS:-1}"
CHAT_TOKENS="${MICROAGENT_HOST_WORKER_DIAGNOSTIC_CHAT_TOKENS:-16}"
STREAM_TOKENS="${MICROAGENT_HOST_WORKER_DIAGNOSTIC_STREAM_TOKENS:-32}"
CURL_TIMEOUT="${MICROAGENT_HOST_WORKER_DIAGNOSTIC_CURL_TIMEOUT:-180}"
KEEP_FAILED="${MICROAGENT_HOST_WORKER_DIAGNOSTIC_KEEP_FAILED:-0}"
BROKER_HOST="${MICROAGENT_HOST_WORKER_MEDIATION_BROKER_HOST:-127.0.0.1}"
BROKER_PORT="${MICROAGENT_HOST_WORKER_MEDIATION_BROKER_PORT:-0}"
BROKER_TIMEOUT="${MICROAGENT_HOST_WORKER_MEDIATION_BROKER_TIMEOUT:-180}"
BROKER_LOG=""
BROKER_STDOUT=""
BROKER_STDERR=""
BROKER_PID=""
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
  python3 "$ROOT/scripts/dev/microagent-host-worker-broker.py" \
    --target-base-url "$target_base_url" \
    --bind-host "$BROKER_HOST" \
    --bind-port "$BROKER_PORT" \
    --log-path "$BROKER_LOG" \
    --timeout "$BROKER_TIMEOUT" >"$BROKER_STDOUT" 2>"$BROKER_STDERR" &
  BROKER_PID="$!"
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
    if [ "$i" -ge "$MEASURE_WARMUPS" ]; then
      sample_index=$((i - MEASURE_WARMUPS))
      printf '{"lane":"%s","endpoint":"%s","sample_index":%s,"connect_seconds":%s,"pretransfer_seconds":%s,"ttfb_seconds":%s,"total_seconds":%s,"response_bytes":%s,"status":%s,"chunks":%s,"request_bytes":%s}\n' \
        "$MEASURE_LANE" "$endpoint" "$sample_index" "$connect" "$pretransfer" "$ttfb" "$total_time" "$response_bytes" "$status" "$chunks" "$request_bytes"
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
  MEASURE_LANE="$lane" \
    MEASURE_BASE_URL="$base_url" \
    MEASURE_PAYLOAD_DIR="$OUT_DIR/payloads" \
    MEASURE_SAMPLES="$SAMPLES" \
    MEASURE_WARMUPS="$WARMUPS" \
    MEASURE_CURL_TIMEOUT="$CURL_TIMEOUT" \
    sh -c "$MEASURE_SH" >"$out"
}

run_guest_lane() {
  local lane="$1"
  local base_url="$2"
  local out="$3"
  "$CLI" exec "$WORKSPACE" --state-dir "$STATE_DIR" --timeout "$CURL_TIMEOUT"s \
    -env "MEASURE_LANE=$lane" \
    -env "MEASURE_BASE_URL=$base_url" \
    -env "MEASURE_PAYLOAD_DIR=/tmp/microagent-host-worker-diagnostic" \
    -env "MEASURE_SAMPLES=$SAMPLES" \
    -env "MEASURE_WARMUPS=$WARMUPS" \
    -env "MEASURE_CURL_TIMEOUT=$CURL_TIMEOUT" \
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

def median(values):
    clean = sorted(float(value) for value in values)
    if not clean:
        return None
    return round(statistics.median(clean), 3)

def ms(row, key):
    return float(row[key]) * 1000

def summarize(lane, endpoint):
    samples = [row for row in rows if row.get("lane") == lane and row.get("endpoint") == endpoint]
    if not samples:
        return None
    payload = payload_manifest.get(endpoint) or {}
    return {
        "lane": lane,
        "endpoint": endpoint,
        "sample_count": len(samples),
        "request_bytes": samples[0].get("request_bytes"),
        "payload_bytes": payload.get("bytes"),
        "payload_sha256": payload.get("sha256"),
        "status_codes": ",".join(sorted({str(row.get("status")) for row in samples})),
        "median_ms": median(ms(row, "total_seconds") for row in samples),
        "ttfb_median_ms": median(ms(row, "ttfb_seconds") for row in samples),
        "connect_median_ms": median(ms(row, "connect_seconds") for row in samples),
        "pretransfer_median_ms": median(ms(row, "pretransfer_seconds") for row in samples),
        "body_read_median_ms": median(max(0.0, ms(row, "total_seconds") - ms(row, "ttfb_seconds")) for row in samples),
        "response_bytes_median": median(row.get("response_bytes") for row in samples),
        "chunks_median": median(row.get("chunks") for row in samples),
    }

summary_rows = []
summary_map = {}
for lane in lanes:
    for endpoint in endpoints:
        item = summarize(lane, endpoint)
        if item:
            summary_rows.append(item)
            summary_map[(lane, endpoint)] = item

summary_fields = (
    "lane",
    "endpoint",
    "sample_count",
    "request_bytes",
    "payload_bytes",
    "payload_sha256",
    "status_codes",
    "median_ms",
    "ttfb_median_ms",
    "connect_median_ms",
    "pretransfer_median_ms",
    "body_read_median_ms",
    "response_bytes_median",
    "chunks_median",
)
with summary_path.open("w", encoding="utf-8", newline="") as f:
    writer = csv.DictWriter(f, fieldnames=summary_fields, delimiter="\t", lineterminator="\n")
    writer.writeheader()
    writer.writerows(summary_rows)

def diff(left, right, field):
    left_item = summary_map.get((left, endpoint))
    right_item = summary_map.get((right, endpoint))
    if not left_item or not right_item:
        return None
    left_value = left_item.get(field)
    right_value = right_item.get(field)
    if left_value is None or right_value is None:
        return None
    return round(float(right_value) - float(left_value), 3)

comparison_rows = []
for endpoint in endpoints:
    comparison_rows.append({
        "endpoint": endpoint,
        "host_broker_overhead_ms": diff("host-direct", "host-broker", "median_ms"),
        "guest_broker_overhead_ms": diff("guest-direct", "guest-broker", "median_ms"),
        "direct_bridge_overhead_ms": diff("host-direct", "guest-direct", "median_ms"),
        "broker_bridge_overhead_ms": diff("host-broker", "guest-broker", "median_ms"),
        "host_broker_ttfb_overhead_ms": diff("host-direct", "host-broker", "ttfb_median_ms"),
        "guest_broker_ttfb_overhead_ms": diff("guest-direct", "guest-broker", "ttfb_median_ms"),
        "direct_bridge_ttfb_overhead_ms": diff("host-direct", "guest-direct", "ttfb_median_ms"),
        "broker_bridge_ttfb_overhead_ms": diff("host-broker", "guest-broker", "ttfb_median_ms"),
        "host_broker_body_overhead_ms": diff("host-direct", "host-broker", "body_read_median_ms"),
        "guest_broker_body_overhead_ms": diff("guest-direct", "guest-broker", "body_read_median_ms"),
    })

comparison_fields = (
    "endpoint",
    "host_broker_overhead_ms",
    "guest_broker_overhead_ms",
    "direct_bridge_overhead_ms",
    "broker_bridge_overhead_ms",
    "host_broker_ttfb_overhead_ms",
    "guest_broker_ttfb_overhead_ms",
    "direct_bridge_ttfb_overhead_ms",
    "broker_bridge_ttfb_overhead_ms",
    "host_broker_body_overhead_ms",
    "guest_broker_body_overhead_ms",
)
with comparison_path.open("w", encoding="utf-8", newline="") as f:
    writer = csv.DictWriter(f, fieldnames=comparison_fields, delimiter="\t", lineterminator="\n")
    writer.writeheader()
    writer.writerows(comparison_rows)
PY
}

write_broker_summary() {
  local log_path="$1"
  local json_path="$2"
  local tsv_path="$3"
  python3 - "$log_path" "$json_path" "$tsv_path" <<'PY'
import csv
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
paths = defaultdict(lambda: {"request_count": 0, "error_count": 0, "response_bytes": 0, "durations_ms": [], "request_bytes": Counter()})
for event in ends:
    item = paths[event.get("path") or ""]
    item["request_count"] += 1
    item["response_bytes"] += int(event.get("response_bytes") or 0)
    item["request_bytes"].update([str(event.get("request_bytes") or 0)])
    if isinstance(event.get("duration_ms"), (int, float)):
        item["durations_ms"].append(float(event["duration_ms"]))
for event in errors:
    item = paths[event.get("path") or ""]
    item["error_count"] += 1
    if isinstance(event.get("duration_ms"), (int, float)):
        item["durations_ms"].append(float(event["duration_ms"]))

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
    rows.append({
        "path": path,
        "request_count": item["request_count"],
        "error_count": item["error_count"],
        "response_bytes": item["response_bytes"],
        "request_bytes_values": ",".join(sorted(item["request_bytes"])),
        "duration_median_ms": median(item["durations_ms"]),
    })
summary = {
    "event_count": len(events),
    "request_count": len(ends),
    "error_count": len(errors),
    "status_counts": dict(Counter(str(event.get("status")) for event in ends if event.get("status") is not None)),
    "paths": rows,
}
json_path.write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
with tsv_path.open("w", encoding="utf-8", newline="") as f:
    writer = csv.DictWriter(
        f,
        fieldnames=("path", "request_count", "error_count", "response_bytes", "request_bytes_values", "duration_median_ms"),
        delimiter="\t",
        lineterminator="\n",
    )
    writer.writeheader()
    writer.writerows(rows)
PY
}

for value_name in SAMPLES WARMUPS CHAT_TOKENS STREAM_TOKENS CURL_TIMEOUT BROKER_PORT; do
  value="${!value_name}"
  case "$value" in
    ''|*[!0-9]*)
      fail "$value_name must be a non-negative integer"
      ;;
  esac
done
if [ "$SAMPLES" -le 0 ]; then
  fail "SAMPLES must be > 0"
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
if [ "$BROKER_PORT" -eq 0 ]; then
  BROKER_PORT="$(choose_port "$BROKER_HOST")"
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
: >"$BROKER_LOG"

WORKER_INFO="$(normalize_worker_url "$HOST_WORKER_URL")" || fail "invalid MICROAGENT_HOST_WORKER_URL"
TARGET_BASE_URL="$(printf '%s' "$WORKER_INFO" | json_get base_url)"
TARGET_BASE_PATH="$(printf '%s' "$WORKER_INFO" | json_get base_path)"
DIRECT_TARGET="$(printf '%s' "$WORKER_INFO" | json_get target)"
BROKER_TARGET="$(broker_connect_host):$BROKER_PORT"
BROKER_URL="http://$(broker_connect_host):$BROKER_PORT$TARGET_BASE_PATH"
BROKER_HEALTH_URL="http://$(broker_connect_host):$BROKER_PORT/healthz"
GUEST_DIRECT_URL="http://127.0.0.1:$DIRECT_GUEST_PORT$TARGET_BASE_PATH"
GUEST_BROKER_URL="http://127.0.0.1:$BROKER_GUEST_PORT$TARGET_BASE_PATH"

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

echo "microagent-host-worker-broker-diagnostic: starting broker $BROKER_URL -> $TARGET_BASE_URL"
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

echo "microagent-host-worker-broker-diagnostic: measuring host-direct"
run_host_lane host-direct "$TARGET_BASE_URL" "$OUT_DIR/host-direct.jsonl"
echo "microagent-host-worker-broker-diagnostic: measuring host-broker"
run_host_lane host-broker "$BROKER_URL" "$OUT_DIR/host-broker.jsonl"
echo "microagent-host-worker-broker-diagnostic: measuring guest-direct"
run_guest_lane guest-direct "$GUEST_DIRECT_URL" "$OUT_DIR/guest-direct.jsonl"
echo "microagent-host-worker-broker-diagnostic: measuring guest-broker"
run_guest_lane guest-broker "$GUEST_BROKER_URL" "$OUT_DIR/guest-broker.jsonl"

cat "$OUT_DIR/host-direct.jsonl" \
  "$OUT_DIR/host-broker.jsonl" \
  "$OUT_DIR/guest-direct.jsonl" \
  "$OUT_DIR/guest-broker.jsonl" >"$OUT_DIR/raw.jsonl"
write_summary "$OUT_DIR/raw.jsonl" "$PAYLOAD_DIR/manifest.json" "$OUT_DIR/summary.tsv" "$OUT_DIR/comparison.tsv"
write_broker_summary "$BROKER_LOG" "$OUT_DIR/broker-summary.json" "$OUT_DIR/broker-summary.tsv"

echo "microagent-host-worker-broker-diagnostic: reports written under $OUT_DIR"
cat "$OUT_DIR/comparison.tsv"
echo "PASS microagent-host-worker-broker-diagnostic"
