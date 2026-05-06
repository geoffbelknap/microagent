#!/usr/bin/env bash
set -euo pipefail

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    echo "firecracker console parity smoke requires Linux amd64" >&2
    exit 2
    ;;
esac

if [ ! -e /dev/kvm ]; then
  echo "/dev/kvm is not visible; run this smoke on a Linux host with KVM" >&2
  exit 2
fi

cat >&2 <<'EOF'
Firecracker console parity is not implemented yet.

This smoke target is intentionally failing until Firecracker supports the same
operator-facing console contract as Apple VF:

- `microagent connect <name> --send "echo CONNECT_READY"` reaches a running
  Firecracker guest shell and returns `CONNECT_READY`.
- interactive `microagent connect <name>` waits for shell readiness by default.
- `Ctrl-]` detaches without stopping the workspace.
- failures clearly distinguish "guest shell is not ready" from "console input
  is unavailable"; serial output remains available through `microagent logs`.

Run this target on the Linux dev side when implementing Firecracker console
input support:

  make smoke-firecracker-console
EOF

exit 1
