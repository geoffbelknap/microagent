#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

run_gate() {
  name="$1"
  shift
  printf '{"gate":"%s","state":"running"}\n' "$name"
  "$@"
  printf '{"gate":"%s","state":"passed"}\n' "$name"
}

run_gate "go-test" scripts/go-test.sh

if [ "${MICROAGENT_STRICT_CONSOLE_PARITY:-0}" = "1" ]; then
  run_gate "firecracker-console-parity" scripts/firecracker-console-parity-smoke.sh
fi

run_gate "firecracker-publish" scripts/firecracker-publish-smoke.sh
run_gate "firecracker-workspace" scripts/firecracker-workspace-smoke.sh
run_gate "firecracker-boot" scripts/firecracker-boot-smoke.sh
