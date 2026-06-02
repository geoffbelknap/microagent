#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$ROOT/scripts/dev/e2e-lib.sh"

# init scaffolds a starter agent body. Pure filesystem output — no VM required.
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-init.XXXXXX")"
CLI="$STATE_DIR/microagent"
cleanup() {
  status="$?"
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_INIT:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E init state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

cd "$ROOT"
export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
e2e_build_cli "$CLI"

assert_file() { [ -f "$1" ] || e2e_fail "expected generated file: $1"; }

e2e_step "init default (anthropic) project"
"$CLI" init demo --dir "$STATE_DIR/demo" >"$STATE_DIR/init.json" 2>&1 || { cat "$STATE_DIR/init.json"; e2e_fail "init"; }
for f in microagent.yaml body.py protocol.py README.md demo/input-001.json demo/system_prompt.md demo/constraints.json; do
  assert_file "$STATE_DIR/demo/$f"
done
grep -q "anthropic" "$STATE_DIR/demo/body.py" || e2e_fail "anthropic body missing provider wiring"

e2e_step "provider variants produce provider-specific bodies"
"$CLI" init op --provider openai --dir "$STATE_DIR/op" >/dev/null 2>&1 || e2e_fail "init openai"
"$CLI" init gm --provider gemini --dir "$STATE_DIR/gm" >/dev/null 2>&1 || e2e_fail "init gemini"
grep -qi "openai" "$STATE_DIR/op/body.py" || e2e_fail "openai body missing provider wiring"
grep -qi "gemini\|generativelanguage\|google" "$STATE_DIR/gm/body.py" || e2e_fail "gemini body missing provider wiring"

e2e_step "invalid provider is rejected"
if "$CLI" init bad --provider nope --dir "$STATE_DIR/bad" >/dev/null 2>&1; then
  e2e_fail "expected unknown provider to fail"
fi

e2e_step "re-init without --force fails, with --force succeeds"
if "$CLI" init demo --dir "$STATE_DIR/demo" >/dev/null 2>&1; then
  e2e_fail "expected re-init without --force to fail"
fi
"$CLI" init demo --dir "$STATE_DIR/demo" --force >/dev/null 2>&1 || e2e_fail "init --force"

e2e_step "generated spec parses as a valid workspace spec"
"$CLI" create --file "$STATE_DIR/demo/microagent.yaml" --dry-run --state-dir "$STATE_DIR/state" >/dev/null 2>&1 \
  || e2e_fail "generated microagent.yaml did not validate via create --dry-run"

e2e_log "init scenario passed"
