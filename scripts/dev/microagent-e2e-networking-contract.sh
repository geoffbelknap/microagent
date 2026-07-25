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
      printf '%s\n' applevf
      ;;
    *)
      printf '%s\n' unsupported
      ;;
  esac
}

BACKEND="${MICROAGENT_E2E_BACKEND:-$(default_backend)}"

case "$BACKEND" in
  linux-kvm)
    exec "$ROOT/scripts/dev/microagent-e2e-networking.sh"
    ;;
  applevf)
    "$ROOT/scripts/dev/applevf-network-mode-smoke.sh"
    "$ROOT/scripts/dev/applevf-publish-smoke.sh"
    "$ROOT/scripts/dev/applevf-cached-nats-e2e.sh"
    ;;
  *)
    e2e_skip "microagent networking E2E does not support backend lane: $BACKEND"
    ;;
esac

echo "microagent E2E networking passed for $BACKEND"
