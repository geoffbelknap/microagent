#!/usr/bin/env bash
set -euo pipefail

# dispatch: one-shot delegated work in a fresh, isolated, single-use workspace.
# Validates the whole dispatch (a) loop end-to-end: boot a throwaway VM under the
# default guarded egress mode, run a command, return the guest result AND the
# mediator-written egress audit (the "what did it do on the network" receipt),
# then tear the workspace down.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-dispatch.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
IMAGE="${MICROAGENT_E2E_DISPATCH_IMAGE:-docker.io/library/busybox:latest}"

cleanup() {
  status="$?"
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_DISPATCH:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E dispatch state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64) ;;
  *) e2e_skip "microagent E2E dispatch requires Linux amd64" ;;
esac

if [ ! -e /dev/kvm ]; then
  e2e_skip "/dev/kvm is not visible; run this smoke outside sandboxed environments"
fi

if [ -n "${MICROAGENT_FIRECRACKER:-}" ]; then
  firecracker="$MICROAGENT_FIRECRACKER"
elif command -v firecracker >/dev/null 2>&1; then
  firecracker="$(command -v firecracker)"
else
  firecracker=""
fi
if [ ! -x "${firecracker:-}" ]; then
  e2e_skip "Linux microagent E2E requires the Firecracker backend binary; install firecracker on PATH or set MICROAGENT_FIRECRACKER"
fi

export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
export GOFLAGS="${GOFLAGS:-} -modcacherw"
export MICROAGENT_FIRECRACKER="$firecracker"
export MICROAGENT_FIRECRACKER_SUPERVISOR="$SUPERVISOR"

echo "microagent E2E suite: dispatch"
echo "==> dispatch"

go build -buildvcs=false -o "$CLI" ./cmd/microagent
go build -buildvcs=false -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit

# Install the guest kernel to its default location; dispatch (via Run) picks it
# up without an explicit --kernel.
"$CLI" kernel install --backend linux-kvm --arch amd64 >"$STATE_DIR/kernel-install.json"

# Dispatch one task under the default guarded mode. It prints a marker and
# fetches a public host, so the mediator-written audit has a real allow decision
# to hand back to the caller.
"$CLI" --json dispatch \
  --image "$IMAGE" \
  --exec "echo dispatch-ok; wget -T 5 -qO- http://example.com >/dev/null 2>&1 && echo fetched || echo no-fetch" \
  --state-dir "$STATE_DIR" >"$STATE_DIR/dispatch.json"

python3 - "$STATE_DIR/dispatch.json" <<'PY'
import json
import sys

with open(sys.argv[1]) as f:
    r = json.load(f)

res = r.get("result") or {}
assert res.get("exit_code") == 0, f"expected exit_code 0, got {res.get('exit_code')}"
assert "dispatch-ok" in (res.get("stdout") or ""), f"marker missing from stdout: {res.get('stdout')!r}"

audit = r.get("audit") or {}
assert audit.get("decision_count", 0) > 0, f"audit summary is empty: {audit}"
allow = audit.get("allow_by_host") or {}
assert any("example.com" in host for host in allow), \
    f"expected example.com in the egress audit allow_by_host receipt, got: {allow}"

print("dispatch result + egress audit receipt OK")
PY

# dispatch is one-shot: the workspace must be torn down, leaving no state behind.
leftover="$(ls "$STATE_DIR/workspaces" 2>/dev/null || true)"
if [ -n "$leftover" ]; then
  echo "FAIL: dispatch left workspace state behind: $leftover" >&2
  exit 1
fi

echo "microagent E2E dispatch passed"
