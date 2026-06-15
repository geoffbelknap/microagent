#!/usr/bin/env bash
#
# microagent-host-worker-topology-sweep.sh - run host-worker probes across guest workspace counts.
#
# This wrapper is runner-neutral. It expects MICROAGENT_HOST_WORKER_URL to
# point at an existing OpenAI-compatible host worker and varies the number of
# guest workspaces that share that worker through the normal probe path.
#
# Required:
#   MICROAGENT_HOST_WORKER_URL                  OpenAI-compatible runner base URL
#
# Optional:
#   MICROAGENT_FIRECRACKER                    Firecracker binary for Linux runs
#   MICROAGENT_HOST_WORKER_TOPOLOGY_WORKSPACES  comma-separated workspace counts (default: 1,2)
#   MICROAGENT_HOST_WORKER_TOPOLOGY_OUT_DIR     report directory (default: /tmp/microagent-host-worker-topology-sweep-$$)
#   MICROAGENT_HOST_WORKER_TOPOLOGY_LABEL       report label prefix (default: topology)
#
# Also accepts the MICROAGENT_HOST_WORKER_PROBE_* environment used by
# microagent-host-worker-probe.sh for samples, warmups, concurrency, telemetry,
# prompts, model selection, and guest setup.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

HOST_WORKER_URL="${MICROAGENT_HOST_WORKER_URL:-}"
WORKSPACES="${MICROAGENT_HOST_WORKER_TOPOLOGY_WORKSPACES:-1,2}"
OUT_DIR="${MICROAGENT_HOST_WORKER_TOPOLOGY_OUT_DIR:-/tmp/microagent-host-worker-topology-sweep-$$}"
LABEL="${MICROAGENT_HOST_WORKER_TOPOLOGY_LABEL:-topology}"

skip() { e2e_skip "microagent-host-worker-topology-sweep: $1"; }
fail() { echo "FAIL microagent-host-worker-topology-sweep: $1" >&2; exit 1; }

if [ -z "$HOST_WORKER_URL" ]; then
  fail "MICROAGENT_HOST_WORKER_URL must point at an OpenAI-compatible host worker"
fi
if [ -z "${MICROAGENT_FIRECRACKER:-}" ]; then
  MICROAGENT_FIRECRACKER="$(e2e_resolve_firecracker)" || skip "Firecracker binary not resolved"
  export MICROAGENT_FIRECRACKER
elif [ ! -x "${MICROAGENT_FIRECRACKER:-/nonexistent}" ]; then
  skip "MICROAGENT_FIRECRACKER not executable: $MICROAGENT_FIRECRACKER"
fi

WORKSPACE_SPACES="$(printf '%s' "$WORKSPACES" | tr ',' ' ')"
if [ -z "$(printf '%s' "$WORKSPACE_SPACES" | tr -d '[:space:]')" ]; then
  fail "MICROAGENT_HOST_WORKER_TOPOLOGY_WORKSPACES must include at least one positive integer"
fi
for workspace_count in $WORKSPACE_SPACES; do
  case "$workspace_count" in
    ''|*[!0-9]*)
      fail "MICROAGENT_HOST_WORKER_TOPOLOGY_WORKSPACES must be comma-separated positive integers"
      ;;
  esac
  if [ "$workspace_count" -le 0 ]; then
    fail "MICROAGENT_HOST_WORKER_TOPOLOGY_WORKSPACES values must be > 0"
  fi
done

mkdir -p "$OUT_DIR"

reports=()
for workspace_count in $WORKSPACE_SPACES; do
  report="$OUT_DIR/workspaces-$workspace_count.json"
  reports+=("$report")

  echo "microagent-host-worker-topology-sweep: probing workspaces=$workspace_count at $HOST_WORKER_URL"
  MICROAGENT_HOST_WORKER_URL="$HOST_WORKER_URL" \
    MICROAGENT_HOST_WORKER_LABEL="$LABEL-workspaces-$workspace_count" \
    MICROAGENT_HOST_WORKER_PROBE_WORKSPACES="$workspace_count" \
    MICROAGENT_HOST_WORKER_PROBE_REPORT="$report" \
    "$ROOT/scripts/dev/microagent-host-worker-probe.sh" || fail "probe failed for workspaces=$workspace_count"
done

echo "microagent-host-worker-topology-sweep: reports written under $OUT_DIR"
"$ROOT/scripts/dev/microagent-host-worker-report-summary.py" "${reports[@]}" >"$OUT_DIR/summary.tsv"
cat "$OUT_DIR/summary.tsv"
"$ROOT/scripts/dev/microagent-host-worker-report-summary.py" --pressure "${reports[@]}" >"$OUT_DIR/pressure.tsv"
cat "$OUT_DIR/pressure.tsv"
echo "PASS microagent-host-worker-topology-sweep"
