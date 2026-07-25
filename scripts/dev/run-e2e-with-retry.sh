#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 SCENARIO" >&2
  exit 2
fi

scenario="$1"
attempt_dir="${MICROAGENT_E2E_ATTEMPT_DIR:-${RUNNER_TEMP:-/tmp}/microagent-e2e-attempts}"
failure_log="$attempt_dir/${scenario//\//-}-first-attempt.log"
current_log="$attempt_dir/${scenario//\//-}-current.log"
mkdir -p "$attempt_dir"
rm -f "$failure_log" "$current_log"

set +e
scripts/dev/microagent-e2e.sh "$scenario" 2>&1 | tee "$current_log"
first_status="${PIPESTATUS[0]}"
set -e
if [ "$first_status" -eq 0 ]; then
  rm -f "$current_log"
  exit 0
fi

mv "$current_log" "$failure_log"
echo "::warning title=E2E retry::$scenario failed its first attempt (exit $first_status); retrying once"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  # shellcheck disable=SC2016 # Markdown backticks are intentional.
  printf '### E2E retry\n\n- `%s` failed its first attempt with exit code `%s` and was retried once.\n' \
    "$scenario" "$first_status" >> "$GITHUB_STEP_SUMMARY"
fi

scripts/dev/microagent-e2e.sh "$scenario"
