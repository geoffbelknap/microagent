#!/usr/bin/env bash
set -euo pipefail

# cred-swap: the `--cred-swap <provider>` convenience surface over credential
# swap. This proves the VM-level wiring end-to-end through the CLI:
#   - `--cred-swap anthropic` resolves the built-in provider registry, GENERATES
#     a per-workspace cred-swap.yaml, allowlists the provider host, and points the
#     workspace's EgressSwapConfigPath at the generated file;
#   - the workspace boots under strict egress, which means the mediator LOADED the
#     generated swap config (it fails closed on a missing/invalid one) — so a
#     successful boot is itself proof the generated file is valid and wired in;
#   - the generated file and the manifest persist the resolved entry + allowlist.
#
# The credential-injection data path (secret never crosses the boundary, real key
# rendered into the provider's own header) is proven hermetically in-process by
# internal/egress: TestE2E_CredentialSwap_SecretNeverCrossesBoundary and
# TestE2E_CredSwapProviderEntry_InjectsProviderHeader. This scenario covers the
# CLI -> lifecycle -> mediator wiring those unit tests cannot reach, and stays
# hermetic (the guest makes no outbound request).

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-cred-swap.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
WORKSPACE="cred-swap"
IMAGE="${MICROAGENT_E2E_CRED_SWAP_IMAGE:-docker.io/library/busybox:latest}"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    "$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_CRED_SWAP:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E cred-swap state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64) ;;
  *) e2e_skip "microagent E2E cred-swap requires Linux amd64" ;;
esac

if [ ! -e /dev/kvm ]; then
  e2e_skip "/dev/kvm is not visible; run this smoke outside sandboxed environments"
fi
command -v python3 >/dev/null 2>&1 || e2e_skip "python3 is required for the cred-swap E2E"

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

echo "microagent E2E suite: cred-swap"
echo "==> cred-swap"

go build -buildvcs=false -o "$CLI" ./cmd/microagent
go build -buildvcs=false -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit

"$CLI" kernel install --backend linux-kvm --arch amd64 >"$STATE_DIR/kernel-install.json"

# Run under STRICT egress with --cred-swap. --keep retains the workspace so we can
# inspect the generated cred-swap.yaml and the persisted manifest. A successful
# boot under strict egress proves the mediator loaded the generated swap config
# (it fails closed on a missing/invalid one). The guest makes no outbound request
# — this scenario is hermetic; injection itself is unit-tested in internal/egress.
"$CLI" --mode=ax run \
  --name "$WORKSPACE" \
  --image "$IMAGE" \
  --egress strict \
  --cred-swap anthropic \
  --keep \
  --exec "echo cred-swap-ok" \
  --state-dir "$STATE_DIR" >"$STATE_DIR/run.json"

python3 - \
  "$STATE_DIR/run.json" \
  "$STATE_DIR/workspaces/$WORKSPACE/cred-swap.yaml" \
  "$STATE_DIR/workspaces/$WORKSPACE/workspace.json" <<'PY'
import json, sys

run_path, swap_path, manifest_path = sys.argv[1:4]

with open(run_path) as f:
    run = json.load(f)
res = run.get("result") or {}
assert res.get("exit_code") == 0, f"expected exit_code 0, got {res.get('exit_code')}: {res}"
assert "cred-swap-ok" in (res.get("stdout") or ""), f"marker missing from stdout: {res.get('stdout')!r}"

# The generated cred-swap.yaml must hold the resolved anthropic entry — reference
# only (env:ANTHROPIC_API_KEY), never a literal secret.
with open(swap_path) as f:
    swap_text = f.read()
# Minimal YAML probe without a yaml dependency: assert the salient fields are present.
for needle in ("anthropic", "static", "x-api-key", "env:ANTHROPIC_API_KEY", "api.anthropic.com"):
    assert needle in swap_text, f"generated cred-swap.yaml missing {needle!r}:\n{swap_text}"
assert "sk-ant" not in swap_text, f"a literal secret leaked into cred-swap.yaml:\n{swap_text}"

# The manifest must persist the generated path and the allowlisted provider host
# so restart/restore re-arm the mediator with the same swap + reachability.
with open(manifest_path) as f:
    manifest = json.load(f)
swap_cfg = manifest.get("egress_swap_config_path") or ""
assert swap_cfg.endswith("cred-swap.yaml"), f"manifest egress_swap_config_path = {swap_cfg!r}, want the generated cred-swap.yaml"
allow = manifest.get("egress_allow") or []
assert "api.anthropic.com" in allow, f"manifest egress_allow = {allow}, want api.anthropic.com unioned in"

print("cred-swap: --cred-swap generated a valid swap config, booted under strict, persisted entry + allowlist")
PY

echo "microagent E2E cred-swap passed"
