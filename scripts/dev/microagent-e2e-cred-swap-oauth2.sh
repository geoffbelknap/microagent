#!/usr/bin/env bash
set -euo pipefail

# cred-swap-oauth2 proves the complete oauth2-cc path through a real Firecracker
# guest and mediator. Hermetic token/resource services listen on host loopback;
# a test-local pasta wrapper maps one guest-visible TEST-NET address back to
# loopback. Two guest TLS requests traverse the tap, transparent redirect, MITM,
# token acquisition, and credential injection. The second request must reuse the
# mediator's cached token.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"
STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-cred-swap-oauth2.XXXXXX")"
CLI="$STATE_DIR/microagent"
SUPERVISOR="$STATE_DIR/microagent-firecracker-supervisor"
GUEST_INIT="$STATE_DIR/microagent-guestinit-amd64"
WORKSPACE="cred-swap-oauth2"
IMAGE="${MICROAGENT_E2E_CRED_SWAP_OAUTH2_IMAGE:-docker.io/curlimages/curl:latest}"
SWAP_DOMAIN="api.oauth2-e2e.example.com"
HOST_MAP_ADDR="192.0.2.2"
TOKEN_PORT=18084
RESOURCE_PORT=18443
CLIENT_ID="oauth2-e2e-client"
CLIENT_SECRET="oauth2-e2e-client-secret"
ACCESS_TOKEN="oauth2-e2e-minted-token"
SERVER_PID=""

cleanup() {
  status="$?"
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" >/dev/null 2>&1 || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
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
for required in python3 openssl pasta; do
  command -v "$required" >/dev/null 2>&1 || e2e_skip "$required is required for the cred-swap-oauth2 E2E"
done
if ! pasta --help 2>&1 | grep -q -- '--map-host-loopback'; then
  e2e_skip "pasta lacks --map-host-loopback, required for hermetic host services"
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

echo "microagent E2E suite: cred-swap-oauth2"
echo "==> cred-swap-oauth2"

go build -buildvcs=false -o "$CLI" ./cmd/microagent
go build -buildvcs=false -o "$SUPERVISOR" ./cmd/microagent-firecracker-supervisor
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -o "$GUEST_INIT" ./cmd/microagent-guestinit

"$CLI" kernel install --backend linux-kvm --arch amd64 >"$STATE_DIR/kernel-install.json"

# Create a private test CA and a leaf for the protected resource. The mediator
# trusts the combined system+test bundle for its upstream TLS leg; the guest
# trusts only the per-workspace MITM CA delivered by guestinit.
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -subj '/CN=microagent oauth2 e2e CA' \
  -keyout "$STATE_DIR/upstream-ca-key.pem" \
  -out "$STATE_DIR/upstream-ca.pem" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes \
  -subj "/CN=$SWAP_DOMAIN" \
  -addext "subjectAltName=DNS:$SWAP_DOMAIN" \
  -keyout "$STATE_DIR/resource-key.pem" \
  -out "$STATE_DIR/resource.csr" >/dev/null 2>&1
printf 'subjectAltName=DNS:%s\n' "$SWAP_DOMAIN" >"$STATE_DIR/resource.ext"
openssl x509 -req -days 1 \
  -in "$STATE_DIR/resource.csr" \
  -CA "$STATE_DIR/upstream-ca.pem" \
  -CAkey "$STATE_DIR/upstream-ca-key.pem" \
  -CAcreateserial \
  -extfile "$STATE_DIR/resource.ext" \
  -out "$STATE_DIR/resource.pem" >/dev/null 2>&1
system_bundle=/etc/ssl/certs/ca-certificates.crt
[ -r "$system_bundle" ] || system_bundle=/etc/pki/tls/certs/ca-bundle.crt
[ -r "$system_bundle" ] || e2e_skip "no system CA bundle found for the mediator"
cp "$system_bundle" "$STATE_DIR/upstream-ca-bundle.pem"
chmod u+w "$STATE_DIR/upstream-ca-bundle.pem"
printf '\n' >>"$STATE_DIR/upstream-ca-bundle.pem"
sed -n '/-----BEGIN CERTIFICATE-----/,/-----END CERTIFICATE-----/p' \
  "$STATE_DIR/upstream-ca.pem" >>"$STATE_DIR/upstream-ca-bundle.pem"

python3 "$ROOT/scripts/dev/oauth2-e2e-server.py" \
  --token-port "$TOKEN_PORT" \
  --resource-port "$RESOURCE_PORT" \
  --cert "$STATE_DIR/resource.pem" \
  --key "$STATE_DIR/resource-key.pem" \
  --events "$STATE_DIR/server-events.jsonl" \
  --client-id "$CLIENT_ID" \
  --client-secret "$CLIENT_SECRET" \
  --token "$ACCESS_TOKEN" >"$STATE_DIR/server.log" 2>&1 &
SERVER_PID="$!"
deadline=$((SECONDS + 10))
until grep -q oauth2_e2e_ready "$STATE_DIR/server.log" 2>/dev/null; do
  kill -0 "$SERVER_PID" 2>/dev/null || { cat "$STATE_DIR/server.log" >&2; exit 1; }
  [ "$SECONDS" -lt "$deadline" ] || { echo "oauth2 E2E services did not become ready" >&2; exit 1; }
  sleep 0.1
done

# Prepend a wrapper rather than changing production pasta arguments. The mapped
# address is visible only inside this test workspace and forwards to host
# loopback, where the two hermetic services listen.
mkdir -p "$STATE_DIR/bin"
real_pasta="$(command -v pasta)"
apply_wrapper="$STATE_DIR/bin/pasta"
printf '#!/bin/sh\nexec %s --map-host-loopback %s "$@"\n' "$real_pasta" "$HOST_MAP_ADDR" >"$apply_wrapper"
chmod 0755 "$apply_wrapper"
export PATH="$STATE_DIR/bin:$PATH"
export E2E_OAUTH2_CLIENT_ID="$CLIENT_ID"
export E2E_OAUTH2_CLIENT_SECRET="$CLIENT_SECRET"
export SSL_CERT_FILE="$STATE_DIR/upstream-ca-bundle.pem"

# A hand-authored oauth2-cc entry: no --cred-swap provider builds this shape,
# so this is the only way to declare one. References only — the mediator
# resolves them on the host when the guest requests the protected resource.
# The hermetic token endpoint is host-local, matching the narrow HTTP loopback
# exception; the guest-facing protected resource remains HTTPS.
cat >"$STATE_DIR/oauth2-swap.yaml" <<EOF
swaps:
  oauth2-e2e:
    type: oauth2-cc
    domains: ["$SWAP_DOMAIN"]
    header: Authorization
    token_url: "http://127.0.0.1:$TOKEN_PORT/token"
    client_id_ref: "env:E2E_OAUTH2_CLIENT_ID"
    client_secret_ref: "env:E2E_OAUTH2_CLIENT_SECRET"
    scopes: ["read", "write"]
EOF

# Run two independent guest TLS connections through the MITM. curl's --resolve
# supplies SNI/Host for the swap domain while routing to the pasta-mapped test
# address. The placeholder is all the guest ever holds.
"$CLI" --json run \
  --name "$WORKSPACE" \
  --image "$IMAGE" \
  --egress mitm \
  --egress-swap-config "$STATE_DIR/oauth2-swap.yaml" \
  --egress-allow "$SWAP_DOMAIN" \
  --keep \
  --exec "curl -sS --resolve '$SWAP_DOMAIN:$RESOURCE_PORT:$HOST_MAP_ADDR' -H 'Authorization: Bearer guest-placeholder' 'https://$SWAP_DOMAIN:$RESOURCE_PORT/first'; curl -sS --resolve '$SWAP_DOMAIN:$RESOURCE_PORT:$HOST_MAP_ADDR' -H 'Authorization: Bearer guest-placeholder' 'https://$SWAP_DOMAIN:$RESOURCE_PORT/second'" \
  --state-dir "$STATE_DIR" >"$STATE_DIR/run.json"

python3 - \
  "$STATE_DIR/run.json" \
  "$STATE_DIR/oauth2-swap.yaml" \
  "$STATE_DIR/workspaces/$WORKSPACE/workspace.json" \
  "$STATE_DIR/server-events.jsonl" \
  "$STATE_DIR/$WORKSPACE/egress-access.jsonl" \
  "$SWAP_DOMAIN" "$CLIENT_SECRET" "$ACCESS_TOKEN" <<'PY'
import json, sys

run_path, swap_path, manifest_path, server_events_path, audit_path, swap_domain, client_secret, access_token = sys.argv[1:9]

with open(run_path) as f:
    run = json.load(f)
res = run.get("result") or {}
assert res.get("exit_code") == 0, f"expected exit_code 0, got {res.get('exit_code')}: {res}"
assert (res.get("stdout") or "").count("oauth2-live-ok") == 2, f"two protected-resource responses missing: {res.get('stdout')!r}"

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

events = [json.loads(line) for line in open(server_events_path) if line.strip()]
token_events = [event for event in events if event.get("event") == "token"]
resource_events = [event for event in events if event.get("event") == "resource"]
assert len(token_events) == 1, f"token endpoint called {len(token_events)} times, expected one cached acquisition: {events}"
assert token_events[0].get("valid") is True, f"token request did not carry expected client-credentials form: {token_events[0]}"
assert len(resource_events) == 2 and all(event.get("valid") for event in resource_events), f"resource did not receive minted bearer twice: {events}"
assert not any(event.get("placeholder_received") for event in resource_events), f"guest placeholder reached protected resource: {events}"

audit_text = open(audit_path, encoding="utf-8", errors="replace").read()
guest_visible = json.dumps(run, sort_keys=True) + "\n" + json.dumps(manifest, sort_keys=True) + "\n" + audit_text
for secret in (client_secret, access_token):
    assert secret not in guest_visible, f"secret/token leaked into guest-visible output, manifest, or mediator audit: {secret!r}"
audit_events = [json.loads(line) for line in audit_text.splitlines() if line.strip()]
swaps = [event for event in audit_events if event.get("event") == "egress_swap" and event.get("type") == "oauth2-cc"]
assert len(swaps) == 2, f"expected two audited oauth2-cc swaps without values: {audit_events}"

print("cred-swap-oauth2: live guest MITM acquired once, injected twice, reused cache, and leaked no credential")
PY

echo "microagent E2E cred-swap-oauth2 passed"
