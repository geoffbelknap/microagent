#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64)
    ;;
  *)
    echo "Linux parity handoff requires Linux amd64" >&2
    exit 2
    ;;
esac

if [ ! -e /dev/kvm ]; then
  echo "/dev/kvm is not visible; run this outside sandboxed environments on a KVM-capable host" >&2
  exit 2
fi

cd "$ROOT"

echo "microagent-kit Linux parity handoff"
echo "repo: $ROOT"
echo "branch: $(git rev-parse --abbrev-ref HEAD)"
echo "head: $(git rev-parse --short HEAD)"
echo

echo "== Static checks =="
go test ./...

echo
echo "== Firecracker boot smoke =="
scripts/firecracker-boot-smoke.sh

echo
echo "== Firecracker workspace smoke =="
scripts/firecracker-workspace-smoke.sh

echo
echo "== Firecracker console parity gate =="
set +e
scripts/firecracker-console-parity-smoke.sh
status="$?"
set -e
if [ "$status" -eq 0 ]; then
  echo "Firecracker console parity gate passed."
elif [ "${MICROAGENT_STRICT_CONSOLE_PARITY:-0}" = "1" ]; then
  echo "Firecracker console parity gate failed in strict mode." >&2
  exit "$status"
elif [ "$status" -eq 1 ]; then
  cat >&2 <<'EOF'
Firecracker console parity is the expected current blocker.

Implement Firecracker console input support until this passes:

  MICROAGENT_STRICT_CONSOLE_PARITY=1 scripts/linux-parity-handoff.sh

Required parity contract:

- `microagent connect <name> --send "echo CONNECT_READY"` reaches the guest.
- interactive `microagent connect <name>` waits for shell readiness by default.
- `Ctrl-]` detaches without stopping the workspace.
- failures distinguish "guest shell is not ready" from "console input is unavailable".
- serial output remains available through `microagent logs`.

After console parity, add and run a live TCP publish smoke for Firecracker
`--publish`, then capture `microagent perf boot`, `perf footprint`, and
`perf steady` numbers on this host.
EOF
else
  echo "Firecracker console parity preflight failed with status $status." >&2
  exit "$status"
fi

echo
echo "Linux parity handoff complete through current known blocker."
