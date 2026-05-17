#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

default_backend() {
  case "$(uname -s):$(uname -m)" in
    Linux:x86_64|Linux:amd64)
      printf '%s\n' firecracker
      ;;
    Darwin:arm64)
      printf '%s\n' applevf
      ;;
    *)
      printf '%s\n' unsupported
      ;;
  esac
}

BACKEND="${MICROAGENT_E2E_BACKEND:-$(default_backend)}"

case "$BACKEND" in
  firecracker)
    exec "$ROOT/scripts/dev/microagent-e2e-mediation.sh"
    ;;
  applevf)
    exec "$ROOT/scripts/dev/applevf-vsock-diagnostic-smoke.sh"
    ;;
  *)
    echo "microagent transport E2E does not support backend lane: $BACKEND" >&2
    exit 2
    ;;
esac
