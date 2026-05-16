#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export PATH="/home/linuxbrew/.linuxbrew/bin:/home/linuxbrew/.linuxbrew/sbin:/opt/homebrew/bin:/opt/homebrew/sbin:$PATH"

INSTALL_DIR="${MICROAGENT_E2E_NETWORK_INSTALL_DIR:-$ROOT/.cache/microagent-e2e/bin}"
SUPERVISOR="$INSTALL_DIR/microagent-firecracker-supervisor"
BRIDGE_NAME="${MICROAGENT_E2E_BRIDGE:-microagent0}"

usage() {
  cat <<'EOF'
microagent-e2e-linux-network-setup.sh

Prepares Linux host prerequisites for microagent E2E NAT and bridged scenarios.
Run this once per checkout, then normal E2E runs can use:

  scripts/dev/microagent-e2e.sh networking

Usage:
  scripts/dev/microagent-e2e-linux-network-setup.sh [--check]

Options:
  --check   Print current host prerequisite status without changing the host.

Environment:
  MICROAGENT_E2E_NETWORK_INSTALL_DIR  Directory for the capability-enabled supervisor
  MICROAGENT_E2E_BRIDGE               Reusable Linux bridge name (default: microagent0)
EOF
}

CHECK_ONLY=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --check)
      CHECK_ONLY=1
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    echo "microagent E2E Linux network setup requires Linux amd64" >&2
    exit 2
    ;;
esac

for required in go ip sysctl getcap; do
  if ! command -v "$required" >/dev/null 2>&1; then
    echo "$required is required for microagent E2E Linux network setup" >&2
    exit 2
  fi
done
if ! command -v setcap >/dev/null 2>&1; then
  echo "setcap is required for microagent E2E Linux network setup" >&2
  exit 2
fi

check_status() {
  echo "microagent E2E Linux network setup check"
  echo "supervisor: $SUPERVISOR"
  if [ -e "$SUPERVISOR" ]; then
    if caps="$(getcap "$SUPERVISOR" 2>/dev/null)"; then
      if [[ "$caps" == *cap_net_admin* ]] && [[ "$caps" == *cap_setpcap* ]]; then
        echo "capabilities: ok ($caps)"
      else
        echo "capabilities: missing cap_net_admin or cap_setpcap ($caps)"
      fi
    else
      echo "capabilities: unreadable"
    fi
  else
    echo "capabilities: supervisor not built"
  fi

  ip_forward="$(sysctl -n net.ipv4.ip_forward 2>/dev/null || true)"
  if [ "$ip_forward" = "1" ]; then
    echo "ip_forward: ok"
  else
    echo "ip_forward: disabled (${ip_forward:-unknown})"
  fi

  echo "bridge: $BRIDGE_NAME"
  if ip link show "$BRIDGE_NAME" >/dev/null 2>&1; then
    bridge_type="$(cat "/sys/class/net/$BRIDGE_NAME/type" 2>/dev/null || true)"
    if [ "$bridge_type" = "772" ]; then
      echo "bridge_type: ok"
    else
      echo "bridge_type: not a Linux bridge (${bridge_type:-unknown})"
    fi
  else
    echo "bridge_type: missing"
  fi
}

if [ "$CHECK_ONLY" = "1" ]; then
  check_status
  exit 0
fi

run_privileged() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
  elif command -v sudo >/dev/null 2>&1; then
    sudo "$@"
  else
    echo "root privileges are required for: $*" >&2
    exit 2
  fi
}

mkdir -p "$INSTALL_DIR"
(
  cd "$ROOT"
  go build -buildvcs=false -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
)

run_privileged setcap cap_net_admin,cap_setpcap+ep "$SUPERVISOR"
if ! caps="$(getcap "$SUPERVISOR" 2>/dev/null)" || [[ "$caps" != *cap_net_admin* ]] || [[ "$caps" != *cap_setpcap* ]]; then
  echo "failed to grant cap_net_admin,cap_setpcap+ep to $SUPERVISOR" >&2
  exit 1
fi

run_privileged sysctl -w net.ipv4.ip_forward=1 >/dev/null

if ip link show "$BRIDGE_NAME" >/dev/null 2>&1; then
  if [ "$(cat "/sys/class/net/$BRIDGE_NAME/type" 2>/dev/null || true)" != "772" ]; then
    echo "$BRIDGE_NAME exists but is not a Linux bridge" >&2
    exit 1
  fi
else
  run_privileged ip link add "$BRIDGE_NAME" type bridge
fi
run_privileged ip link set "$BRIDGE_NAME" up

cat <<EOF
microagent E2E Linux network setup complete
supervisor: $SUPERVISOR
capabilities: $caps
bridge: $BRIDGE_NAME

Normal run:
  scripts/dev/microagent-e2e.sh networking
EOF
