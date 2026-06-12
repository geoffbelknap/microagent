#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

cd "$ROOT"
e2e_windows_hyperv_host_probe MICROAGENT_WINDOWS_HYPERV_SMOKE ./pkg/workspace 'TestWindowsHyperVSmokeRunResult$'
