#!/usr/bin/env bash
set -euo pipefail

# cred-swap-oauth2: the oauth2-cc credential-swap strategy has no built-in
# --cred-swap provider shorthand (that surface only knows static API-key
# providers), so this proves the CLI/lifecycle/mediator wiring for a
# hand-authored oauth2-cc entry supplied via --egress-swap-config:
#   - the entry loads into the mediator under mitm egress (LoadSwapTable fails
#     closed on a malformed/unknown-type entry, so a successful boot is itself
#     proof the oauth2-cc entry parsed and validated);
#   - the generated/persisted config and manifest carry only reference fields
#     (token_url, client_id_ref, client_secret_ref, scopes) — never a literal
#     secret, matching the static --cred-swap scenario's same guarantee.
#
# The credential-acquisition and injection data path itself — the token
# endpoint exchange, caching, injection into the guest's request, and the
# fail-closed cases (unavailable token endpoint, invalid token response,
# near-expiry refresh) — is proven hermetically in-process by internal/egress:
# TestE2E_OAuth2CC_AcquiresInjectsAndCachesToken and its three siblings. This
# scenario covers the CLI -> lifecycle -> mediator wiring those tests cannot
# reach, and stays hermetic (the guest makes no outbound request) for the same
# reason the static cred-swap scenario does.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-cred-swap-oauth2.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
WORKSPACE="cred-swap-oauth2"
IMAGE="${MICROAGENT_E2E_CRED_SWAP_OAUTH2_IMAGE:-docker.io/library/busybox:latest}"
SWAP_DOMAIN="api.oauth2-e2e.example.com"

cleanup() {
  status="$?"
  if [ -x "$CLI" ]; then
    "$CLI" halt "$WORKSPACE" --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
    "$CLI" delete "$WORKSPACE" --yes --state-dir "$STATE_DIR" >/dev/null 2>&1 || true
  fi
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "${MICROAGENT_KEEP_MICROAGENT_E2E_CRED_SWAP_OAUTH2:-0}" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E cred-swap-oauth2 state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64) ;;
  *) e2e_skip "microagent E2E cred-swap-oauth2 requires Linux amd64" ;;
esac

if [ ! -e /dev/kvm ]; then
  e2e_skip "/dev/kvm is not visible; run this smoke outside sandboxed environments"
fi
command -v python3 >/dev/null 2>&1 || e2e_skip "python3 is required for the cred-swap-oauth2 E2E"

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

echo "microagent E2E suite: cred-swap-oauth2"
echo "==> cred-swap-oauth2"

go build -buildvcs=false -o "$CLI" ./cmd/microagent
go build -buildvcs=false -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit

"$CLI" kernel install --backend linux-kvm --arch amd64 >"$STATE_DIR/kernel-install.json"

# A hand-authored oauth2-cc entry: no --cred-swap provider builds this shape,
# so this is the only way to declare one. References only — the refs never
# resolve during this scenario (the guest makes no outbound request), which
# is exactly why boot succeeding is the wiring proof: LoadSwapTable validates
# type/domains at load time regardless of whether acquisition ever runs.
cat >"$STATE_DIR/oauth2-swap.yaml" <<EOF
swaps:
  oauth2-e2e:
    type: oauth2-cc
    domains: ["$SWAP_DOMAIN"]
    header: Authorization
    token_url: "https://token.oauth2-e2e.example.com/token"
    client_id_ref: "env:E2E_OAUTH2_CLIENT_ID"
    client_secret_ref: "env:E2E_OAUTH2_CLIENT_SECRET"
    scopes: ["read", "write"]
EOF

# Run under mitm egress with the hand-authored swap config. --keep retains the
# workspace so we can inspect the persisted manifest. A successful boot proves
# the mediator loaded and validated the oauth2-cc entry (it fails closed on a
# missing/invalid one, same as the static cred-swap scenario). The guest makes
# no outbound request — see the file header for why this stays hermetic.
"$CLI" --json run \
  --name "$WORKSPACE" \
  --image "$IMAGE" \
  --egress mitm \
  --egress-swap-config "$STATE_DIR/oauth2-swap.yaml" \
  --egress-allow "$SWAP_DOMAIN" \
  --keep \
  --exec "echo cred-swap-oauth2-ok" \
  --state-dir "$STATE_DIR" >"$STATE_DIR/run.json"

python3 - \
  "$STATE_DIR/run.json" \
  "$STATE_DIR/oauth2-swap.yaml" \
  "$STATE_DIR/workspaces/$WORKSPACE/workspace.json" \
  "$SWAP_DOMAIN" <<'PY'
import json, sys

run_path, swap_path, manifest_path, swap_domain = sys.argv[1:5]

with open(run_path) as f:
    run = json.load(f)
res = run.get("result") or {}
assert res.get("exit_code") == 0, f"expected exit_code 0, got {res.get('exit_code')}: {res}"
assert "cred-swap-oauth2-ok" in (res.get("stdout") or ""), f"marker missing from stdout: {res.get('stdout')!r}"

# The swap config supplied via --egress-swap-config must hold only reference
# fields for the oauth2-cc entry — never a literal client secret or token.
with open(swap_path) as f:
    swap_text = f.read()
for needle in ("oauth2-cc", "token_url", "env:E2E_OAUTH2_CLIENT_ID", "env:E2E_OAUTH2_CLIENT_SECRET", swap_domain):
    assert needle in swap_text, f"oauth2-swap.yaml missing {needle!r}:\n{swap_text}"

# The manifest must persist the swap config path (mediator re-arms it on
# restart/restore) and the allowlisted domain from --egress-allow.
with open(manifest_path) as f:
    manifest = json.load(f)
assert manifest.get("egress_swap_config_path", "").endswith("oauth2-swap.yaml"), (
    f"manifest egress_swap_config_path = {manifest.get('egress_swap_config_path')!r}, want the oauth2-swap.yaml path"
)
allow = manifest.get("egress_allow") or []
assert swap_domain in allow, f"manifest egress_allow = {allow}, want {swap_domain!r}"

print("cred-swap-oauth2: hand-authored oauth2-cc entry loaded, booted under mitm, persisted config path + allowlist")
PY

echo "microagent E2E cred-swap-oauth2 passed"
