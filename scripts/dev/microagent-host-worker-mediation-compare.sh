#!/usr/bin/env bash
#
# microagent-host-worker-mediation-compare.sh - compare direct vs brokered host-worker access.
#
# This is an experimental measurement harness. It keeps runner launch out of
# scope and expects an already-running OpenAI-compatible worker. The script runs
# the existing host-worker probe twice:
#   1. direct to MICROAGENT_HOST_WORKER_URL
#   2. through a local pass-through broker that logs each request
#
# The broker is not a policy enforcement boundary. It is a narrow surface for
# measuring the latency/streaming overhead of adding a host-side mediation hop
# before we design policy, quotas, admission control, or audit semantics.
#
# Required:
#   MICROAGENT_HOST_WORKER_URL                         OpenAI-compatible worker base URL
#
# Optional:
#   MICROAGENT_FIRECRACKER                             Firecracker binary for Linux runs
#   MICROAGENT_HOST_WORKER_MEDIATION_OUT_DIR           output dir (default: /tmp/microagent-host-worker-mediation-compare-$$)
#   MICROAGENT_HOST_WORKER_MEDIATION_LABEL             report label prefix (default: mediation)
#   MICROAGENT_HOST_WORKER_MEDIATION_BROKER_HOST       broker bind host (default: 127.0.0.1)
#   MICROAGENT_HOST_WORKER_MEDIATION_BROKER_PORT       broker bind port, 0 means auto (default: 0)
#   MICROAGENT_HOST_WORKER_MEDIATION_BROKER_LOG        JSONL audit log path (default: $OUT_DIR/broker.jsonl)
#   MICROAGENT_HOST_WORKER_MEDIATION_BROKER_TIMEOUT    upstream timeout seconds (default: 180)
#   MICROAGENT_HOST_WORKER_MEDIATION_WORKSPACE_PREFIX  workspace prefix for probe runs
#
# Also accepts MICROAGENT_HOST_WORKER_* metadata and the
# MICROAGENT_HOST_WORKER_PROBE_* environment used by
# microagent-host-worker-probe.sh for samples, warmups, concurrency, telemetry,
# prompts, model selection, and guest setup.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

HOST_WORKER_URL="${MICROAGENT_HOST_WORKER_URL:-}"
OUT_DIR="${MICROAGENT_HOST_WORKER_MEDIATION_OUT_DIR:-/tmp/microagent-host-worker-mediation-compare-$$}"
LABEL="${MICROAGENT_HOST_WORKER_MEDIATION_LABEL:-mediation}"
BROKER_HOST="${MICROAGENT_HOST_WORKER_MEDIATION_BROKER_HOST:-127.0.0.1}"
BROKER_PORT="${MICROAGENT_HOST_WORKER_MEDIATION_BROKER_PORT:-0}"
BROKER_LOG="${MICROAGENT_HOST_WORKER_MEDIATION_BROKER_LOG:-}"
BROKER_TIMEOUT="${MICROAGENT_HOST_WORKER_MEDIATION_BROKER_TIMEOUT:-180}"
WORKSPACE_PREFIX="${MICROAGENT_HOST_WORKER_MEDIATION_WORKSPACE_PREFIX:-host-worker-mediation-$$}"
BROKER_PID=""
BROKER_STDOUT=""
BROKER_STDERR=""

skip() { e2e_skip "microagent-host-worker-mediation-compare: $1"; }
fail() { echo "FAIL microagent-host-worker-mediation-compare: $1" >&2; exit 1; }

cleanup() {
  local status=$?
  set +e
  if [ -n "$BROKER_PID" ]; then
    kill "$BROKER_PID" >/dev/null 2>&1 || true
    wait "$BROKER_PID" >/dev/null 2>&1 || true
    BROKER_PID=""
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
base_url = urllib.parse.urlunparse((parsed.scheme, parsed.netloc, path, "", "", ""))
print(json.dumps(
    {
        "base_path": path,
        "base_url": base_url,
        "host": parsed.hostname,
        "port": port,
        "scheme": parsed.scheme,
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

start_broker() {
  local target_base_url="$1"
  local stdout_path="$2"
  local stderr_path="$3"

  python3 "$ROOT/scripts/dev/microagent-host-worker-broker.py" \
    --target-base-url "$target_base_url" \
    --bind-host "$BROKER_HOST" \
    --bind-port "$BROKER_PORT" \
    --log-path "$BROKER_LOG" \
    --timeout "$BROKER_TIMEOUT" >"$stdout_path" 2>"$stderr_path" &
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

run_probe() {
  local mode="$1"
  local url="$2"
  local report="$3"
  local workspace="$4"
  local run_label="$5"
  local health_url="$6"
  local -a env_args

  env_args=(
    "MICROAGENT_HOST_WORKER_URL=$url"
    "MICROAGENT_HOST_WORKER_LABEL=$run_label"
    "MICROAGENT_HOST_WORKER_PROBE_REPORT=$report"
    "MICROAGENT_HOST_WORKER_PROBE_WORKSPACE=$workspace"
    "MICROAGENT_HOST_WORKER_PROBE_PRINT_REPORT=${MICROAGENT_HOST_WORKER_PROBE_PRINT_REPORT:-0}"
  )
  if [ -n "$health_url" ]; then
    env_args+=("MICROAGENT_HOST_WORKER_HEALTH_URL=$health_url")
  fi

  echo "microagent-host-worker-mediation-compare: probing $mode endpoint at $url"
  env "${env_args[@]}" "$ROOT/scripts/dev/microagent-host-worker-probe.sh" || fail "$mode probe failed"
}

annotate_report() {
  local report="$1"
  local mode="$2"
  local worker_url="$3"
  local target_url="$4"
  local broker_log="$5"
  local tmp
  tmp="$(mktemp)"
  python3 - "$report" "$mode" "$worker_url" "$target_url" "$broker_log" <<'PY' >"$tmp"
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
mode, worker_url, target_url, broker_log = sys.argv[2:6]
report = json.loads(path.read_text(encoding="utf-8"))
scope = "measurement-only pass-through broker; not a policy enforcement boundary"
mediation = {
    "schema_version": 1,
    "mode": mode,
    "scope": scope,
    "worker_url": worker_url,
}
if target_url:
    mediation["target_url"] = target_url
if broker_log:
    mediation["audit_log_path"] = broker_log
report.setdefault("host_worker", {})["mediation"] = mediation
diagnostics = report.setdefault("host_worker", {}).setdefault("diagnostics", {})
sources = diagnostics.setdefault("sources", [])
source = "microagent.host_worker_broker" if mode == "host-broker" else "microagent.host_worker_direct"
if source not in sources:
    sources.append(source)
print(json.dumps(report, indent=2, sort_keys=True))
PY
  mv "$tmp" "$report"
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
paths = defaultdict(lambda: {"request_count": 0, "error_count": 0, "response_bytes": 0, "durations_ms": []})
for event in ends:
    item = paths[event.get("path") or ""]
    item["request_count"] += 1
    item["response_bytes"] += int(event.get("response_bytes") or 0)
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
        fieldnames=("path", "request_count", "error_count", "response_bytes", "duration_median_ms"),
        delimiter="\t",
        lineterminator="\n",
    )
    writer.writeheader()
    writer.writerows(rows)
PY
}

write_mediation_comparison() {
  local direct_report="$1"
  local mediated_report="$2"
  local out_path="$3"
  python3 - "$direct_report" "$mediated_report" "$out_path" <<'PY'
import csv
import json
import sys
from pathlib import Path

direct = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
mediated = json.loads(Path(sys.argv[2]).read_text(encoding="utf-8"))
out_path = Path(sys.argv[3])

fields = (
    "endpoint",
    "workspace_count",
    "per_workspace_concurrency",
    "effective_concurrency",
    "direct_guest_median_ms",
    "mediated_guest_median_ms",
    "mediation_guest_overhead_ms",
    "mediation_guest_overhead_pct",
    "direct_host_median_ms",
    "mediated_host_median_ms",
    "mediation_host_overhead_ms",
    "direct_delta_ms",
    "mediated_delta_ms",
    "mediation_bridge_delta_change_ms",
    "direct_guest_p95_ms",
    "mediated_guest_p95_ms",
    "mediation_guest_p95_overhead_ms",
    "direct_p95_delta_ms",
    "mediated_p95_delta_ms",
    "mediation_p95_delta_change_ms",
    "direct_guest_ttfb_median_ms",
    "mediated_guest_ttfb_median_ms",
    "mediation_guest_ttfb_overhead_ms",
    "direct_ttfb_delta_ms",
    "mediated_ttfb_delta_ms",
    "mediation_ttfb_delta_change_ms",
    "direct_guest_body_read_median_ms",
    "mediated_guest_body_read_median_ms",
    "mediation_guest_body_read_overhead_ms",
)

def pct(delta, base):
    if base in (None, 0):
        return None
    return round((delta / base) * 100, 3)

def diff(left, right):
    if left is None or right is None:
        return None
    return round(right - left, 3)

def endpoint_order(keys):
    preferred = ["models", "chat", "stream"]
    present = set(keys)
    return [key for key in preferred if key in present] + sorted(present - set(preferred))

rows = []
levels = sorted(
    set(str(level) for level in direct.get("concurrency_levels") or [])
    & set(str(level) for level in mediated.get("concurrency_levels") or []),
    key=lambda value: int(value),
)
for level in levels:
    direct_level = (direct.get("matrix") or {}).get(level) or {}
    mediated_level = (mediated.get("matrix") or {}).get(level) or {}
    endpoints = endpoint_order(
        set((direct_level.get("guest") or {}).keys())
        & set((mediated_level.get("guest") or {}).keys())
    )
    for endpoint in endpoints:
        direct_guest = (direct_level.get("guest") or {}).get(endpoint) or {}
        mediated_guest = (mediated_level.get("guest") or {}).get(endpoint) or {}
        direct_host = (direct_level.get("host") or {}).get(endpoint) or {}
        mediated_host = (mediated_level.get("host") or {}).get(endpoint) or {}
        direct_overhead = (direct_level.get("overhead") or {}).get(endpoint) or {}
        mediated_overhead = (mediated_level.get("overhead") or {}).get(endpoint) or {}

        guest_overhead = diff(direct_guest.get("median_ms"), mediated_guest.get("median_ms"))
        row = {
            "endpoint": endpoint,
            "workspace_count": mediated.get("workspace_count") or direct.get("workspace_count"),
            "per_workspace_concurrency": int(level),
            "effective_concurrency": mediated_guest.get("concurrency") or direct_guest.get("concurrency"),
            "direct_guest_median_ms": direct_guest.get("median_ms"),
            "mediated_guest_median_ms": mediated_guest.get("median_ms"),
            "mediation_guest_overhead_ms": guest_overhead,
            "mediation_guest_overhead_pct": pct(guest_overhead, direct_guest.get("median_ms")),
            "direct_host_median_ms": direct_host.get("median_ms"),
            "mediated_host_median_ms": mediated_host.get("median_ms"),
            "mediation_host_overhead_ms": diff(direct_host.get("median_ms"), mediated_host.get("median_ms")),
            "direct_delta_ms": direct_overhead.get("delta_ms"),
            "mediated_delta_ms": mediated_overhead.get("delta_ms"),
            "mediation_bridge_delta_change_ms": diff(direct_overhead.get("delta_ms"), mediated_overhead.get("delta_ms")),
            "direct_guest_p95_ms": direct_guest.get("p95_ms"),
            "mediated_guest_p95_ms": mediated_guest.get("p95_ms"),
            "mediation_guest_p95_overhead_ms": diff(direct_guest.get("p95_ms"), mediated_guest.get("p95_ms")),
            "direct_p95_delta_ms": direct_overhead.get("p95_delta_ms"),
            "mediated_p95_delta_ms": mediated_overhead.get("p95_delta_ms"),
            "mediation_p95_delta_change_ms": diff(direct_overhead.get("p95_delta_ms"), mediated_overhead.get("p95_delta_ms")),
            "direct_guest_ttfb_median_ms": direct_guest.get("ttfb_median_ms"),
            "mediated_guest_ttfb_median_ms": mediated_guest.get("ttfb_median_ms"),
            "mediation_guest_ttfb_overhead_ms": diff(direct_guest.get("ttfb_median_ms"), mediated_guest.get("ttfb_median_ms")),
            "direct_ttfb_delta_ms": direct_overhead.get("ttfb_delta_ms"),
            "mediated_ttfb_delta_ms": mediated_overhead.get("ttfb_delta_ms"),
            "mediation_ttfb_delta_change_ms": diff(direct_overhead.get("ttfb_delta_ms"), mediated_overhead.get("ttfb_delta_ms")),
            "direct_guest_body_read_median_ms": direct_guest.get("body_read_median_ms"),
            "mediated_guest_body_read_median_ms": mediated_guest.get("body_read_median_ms"),
            "mediation_guest_body_read_overhead_ms": diff(direct_guest.get("body_read_median_ms"), mediated_guest.get("body_read_median_ms")),
        }
        rows.append(row)

with out_path.open("w", encoding="utf-8", newline="") as f:
    writer = csv.DictWriter(f, fieldnames=fields, delimiter="\t", lineterminator="\n")
    writer.writeheader()
    writer.writerows(rows)
PY
}

case "$BROKER_PORT" in
  ''|*[!0-9]*)
    fail "MICROAGENT_HOST_WORKER_MEDIATION_BROKER_PORT must be a non-negative integer"
    ;;
esac
if [ "$BROKER_PORT" -eq 0 ]; then
  BROKER_PORT="$(choose_port "$BROKER_HOST")"
fi
case "$BROKER_TIMEOUT" in
  ''|*[!0-9.]*)
    fail "MICROAGENT_HOST_WORKER_MEDIATION_BROKER_TIMEOUT must be numeric"
    ;;
esac
if [ -z "$HOST_WORKER_URL" ]; then
  fail "MICROAGENT_HOST_WORKER_URL must point at an OpenAI-compatible host worker"
fi
if [ ! -x "$ROOT/scripts/dev/microagent-host-worker-probe.sh" ]; then
  fail "host worker probe script not executable"
fi
if [ -z "${MICROAGENT_FIRECRACKER:-}" ]; then
  MICROAGENT_FIRECRACKER="$(e2e_resolve_firecracker)" || skip "Firecracker binary not resolved"
  export MICROAGENT_FIRECRACKER
elif [ ! -x "${MICROAGENT_FIRECRACKER:-/nonexistent}" ]; then
  skip "MICROAGENT_FIRECRACKER not executable: $MICROAGENT_FIRECRACKER"
fi

mkdir -p "$OUT_DIR"
if [ -z "$BROKER_LOG" ]; then
  BROKER_LOG="$OUT_DIR/broker.jsonl"
fi
: >"$BROKER_LOG"
BROKER_STDOUT="$OUT_DIR/broker.stdout"
BROKER_STDERR="$OUT_DIR/broker.stderr"

WORKER_INFO="$(normalize_worker_url "$HOST_WORKER_URL")" || fail "invalid MICROAGENT_HOST_WORKER_URL"
TARGET_BASE_URL="$(printf '%s' "$WORKER_INFO" | json_get base_url)"
TARGET_BASE_PATH="$(printf '%s' "$WORKER_INFO" | json_get base_path)"
BROKER_URL="http://$(broker_connect_host):$BROKER_PORT$TARGET_BASE_PATH"
BROKER_HEALTH_URL="http://$(broker_connect_host):$BROKER_PORT/healthz"

DIRECT_REPORT="$OUT_DIR/direct.json"
MEDIATED_REPORT="$OUT_DIR/mediated.json"

echo "microagent-host-worker-mediation-compare: output dir $OUT_DIR"
run_probe direct "$TARGET_BASE_URL" "$DIRECT_REPORT" "$WORKSPACE_PREFIX-direct" "$LABEL-direct" ""
annotate_report "$DIRECT_REPORT" direct "$TARGET_BASE_URL" "" ""

echo "microagent-host-worker-mediation-compare: starting pass-through broker $BROKER_URL -> $TARGET_BASE_URL"
start_broker "$TARGET_BASE_URL" "$BROKER_STDOUT" "$BROKER_STDERR"
wait_for_broker "$BROKER_HEALTH_URL"
run_probe mediated "$BROKER_URL" "$MEDIATED_REPORT" "$WORKSPACE_PREFIX-mediated" "$LABEL-mediated" "$BROKER_URL/models"
annotate_report "$MEDIATED_REPORT" host-broker "$BROKER_URL" "$TARGET_BASE_URL" "$BROKER_LOG"

if [ -n "$BROKER_PID" ]; then
  kill "$BROKER_PID" >/dev/null 2>&1 || true
  wait "$BROKER_PID" >/dev/null 2>&1 || true
  BROKER_PID=""
fi

"$ROOT/scripts/dev/microagent-host-worker-report-summary.py" "$DIRECT_REPORT" "$MEDIATED_REPORT" >"$OUT_DIR/summary.tsv"
"$ROOT/scripts/dev/microagent-host-worker-report-summary.py" --pressure "$DIRECT_REPORT" "$MEDIATED_REPORT" >"$OUT_DIR/pressure.tsv"
"$ROOT/scripts/dev/microagent-host-worker-report-summary.py" --pressure --compact "$DIRECT_REPORT" "$MEDIATED_REPORT" >"$OUT_DIR/pressure-compact.tsv"
write_broker_summary "$BROKER_LOG" "$OUT_DIR/broker-summary.json" "$OUT_DIR/broker-summary.tsv"
write_mediation_comparison "$DIRECT_REPORT" "$MEDIATED_REPORT" "$OUT_DIR/mediation.tsv"

echo "microagent-host-worker-mediation-compare: reports written under $OUT_DIR"
cat "$OUT_DIR/mediation.tsv"
echo "PASS microagent-host-worker-mediation-compare"
