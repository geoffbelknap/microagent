#!/usr/bin/env bash
set -euo pipefail

export MICROAGENT_STRICT_CONSOLE_PARITY=1
exec "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/linux-parity-handoff.sh"
