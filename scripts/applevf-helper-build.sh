#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HELPER_DIR="$ROOT/helpers/applevf"
HELPER_BIN="$HELPER_DIR/.build/release/microagent-applevf-helper"
ENTITLEMENTS="$HELPER_DIR/microagent-applevf-helper.entitlements"

if [ "$(uname -s)" != "Darwin" ]; then
  echo "Apple VF helper build requires macOS" >&2
  exit 1
fi

swift build --package-path "$HELPER_DIR" --configuration release --disable-sandbox
codesign -s - -f --entitlements "$ENTITLEMENTS" "$HELPER_BIN"
"$HELPER_BIN" <<< '{"command":"host"}'
