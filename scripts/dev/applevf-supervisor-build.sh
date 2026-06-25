#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SUPERVISOR_DIR="$ROOT/supervisors/applevf"
SUPERVISOR_BIN="$SUPERVISOR_DIR/.build/release/microagent-applevf-supervisor"
ENTITLEMENTS="$SUPERVISOR_DIR/microagent-applevf-supervisor.entitlements"
SIGN_IDENTITY="${MICROAGENT_APPLEVF_CODESIGN_IDENTITY:--}"
SIGN_OPTIONS="${MICROAGENT_APPLEVF_CODESIGN_OPTIONS:-runtime,library}"

if [ "$(uname -s)" != "Darwin" ]; then
  echo "Apple VF supervisor build requires macOS" >&2
  exit 1
fi

swift build --package-path "$SUPERVISOR_DIR" --configuration release --disable-sandbox
# Ad-hoc signature (-s -) is the default for local/dev. Set
# MICROAGENT_APPLEVF_CODESIGN_IDENTITY to a valid local codesigning identity
# when diagnosing Apple Virtualization.framework behavior that may depend on a
# non-ad-hoc Team ID signature. Keep the entitlement set minimal (only
# com.apple.security.virtualization) — never add the app-sandbox or
# com.apple.vm.networking entitlements here.
codesign -s "$SIGN_IDENTITY" -f --options "$SIGN_OPTIONS" --entitlements "$ENTITLEMENTS" "$SUPERVISOR_BIN"
codesign -d --entitlements :- "$SUPERVISOR_BIN" >/dev/null 2>&1
"$SUPERVISOR_BIN" <<< '{"command":"host"}'
