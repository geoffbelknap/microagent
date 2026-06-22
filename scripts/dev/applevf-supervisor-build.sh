#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SUPERVISOR_DIR="$ROOT/supervisors/applevf"
SUPERVISOR_BIN="$SUPERVISOR_DIR/.build/release/microagent-applevf-supervisor"
ENTITLEMENTS="$SUPERVISOR_DIR/microagent-applevf-supervisor.entitlements"

if [ "$(uname -s)" != "Darwin" ]; then
  echo "Apple VF supervisor build requires macOS" >&2
  exit 1
fi

swift build --package-path "$SUPERVISOR_DIR" --configuration release --disable-sandbox
# Ad-hoc signature (-s -) is intentional for local/dev: a real Developer-ID
# identity + notarization is deferred to a distribution milestone. The hardened
# runtime (-o runtime) and library validation (--options library) are the
# layer-2 confinement posture and work with ad-hoc signing locally. Keep the
# entitlement set minimal (only com.apple.security.virtualization) — never add
# the app-sandbox or com.apple.vm.networking entitlements here.
codesign -s - -f --options runtime,library --entitlements "$ENTITLEMENTS" "$SUPERVISOR_BIN"
"$SUPERVISOR_BIN" <<< '{"command":"host"}'
