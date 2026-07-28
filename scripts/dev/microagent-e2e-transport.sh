#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

default_backend() {
  case "$(uname -s):$(uname -m)" in
    Linux:x86_64|Linux:amd64)
      printf '%s\n' linux-kvm
      ;;
    Darwin:arm64)
      printf '%s\n' apple-vf
      ;;
    *)
      printf '%s\n' unsupported
      ;;
  esac
}

BACKEND="$(e2e_normalize_backend "${MICROAGENT_E2E_BACKEND:-$(default_backend)}")"

case "$BACKEND" in
  linux-kvm)
    exec "$ROOT/scripts/dev/microagent-e2e-mediation.sh"
    ;;
  apple-vf)
    exec "$ROOT/scripts/dev/applevf-vsock-diagnostic-smoke.sh"
    ;;
  *)
    e2e_skip "microagent transport E2E does not support backend lane: $BACKEND"
    ;;
esac
