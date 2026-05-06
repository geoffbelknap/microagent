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
codesign -s - -f --entitlements "$ENTITLEMENTS" "$SUPERVISOR_BIN"
"$SUPERVISOR_BIN" <<< '{"command":"host"}'
