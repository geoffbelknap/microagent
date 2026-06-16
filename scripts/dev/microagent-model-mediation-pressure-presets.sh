#!/usr/bin/env bash
#
# Shared pressure preset defaults for model mediation E2E adapters.
#
# Adapters pass their env prefix, selected preset, and ordinary default value.
# Explicit env vars always win. Presets only provide defaults for pressure mode.

pressure_preset_validate() {
  local preset="$1"
  case "$preset" in
    ''|default|baseline|ci|hardware)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

pressure_preset_value() {
  local prefix="$1"
  local preset="$2"
  local key="$3"
  local fallback="$4"
  local override="${prefix}_${key}"

  if [ "${!override+x}" = "x" ]; then
    printf '%s\n' "${!override}"
    return 0
  fi

  case "$preset:$key" in
    ci:PRESSURE_WORKSPACES) printf '%s\n' 1 ;;
    ci:PRESSURE_CONCURRENCY) printf '%s\n' 1 ;;
    ci:PRESSURE_CASES) printf '%s\n' direct,local,pf,pa ;;
    ci:PRESSURE_SAMPLES) printf '%s\n' 1 ;;
    ci:PRESSURE_WARMUPS) printf '%s\n' 0 ;;
    ci:PRESSURE_CHAT_TOKENS) printf '%s\n' 16 ;;
    ci:PRESSURE_STREAM_TOKENS) printf '%s\n' 16 ;;
    ci:PRESSURE_TELEMETRY) printf '%s\n' off ;;
    ci:PRESSURE_GATE_MODE) printf '%s\n' required ;;
    ci:PRESSURE_MAX_MODELS_TOTAL_P95_DELTA_MS) printf '%s\n' 100 ;;
    ci:PRESSURE_MAX_CHAT_TOTAL_P95_DELTA_MS) printf '%s\n' 250 ;;
    ci:PRESSURE_MAX_STREAM_TTFB_P95_DELTA_MS) printf '%s\n' 100 ;;
    ci:PRESSURE_MAX_DECISION_P95_MS) printf '%s\n' 50 ;;

    hardware:PRESSURE_WORKSPACES) printf '%s\n' 1 ;;
    hardware:PRESSURE_CONCURRENCY) printf '%s\n' 1,2 ;;
    hardware:PRESSURE_CASES) printf '%s\n' direct,local,pf,pa ;;
    hardware:PRESSURE_SAMPLES) printf '%s\n' 1 ;;
    hardware:PRESSURE_WARMUPS) printf '%s\n' 0 ;;
    hardware:PRESSURE_CHAT_TOKENS) printf '%s\n' 16 ;;
    hardware:PRESSURE_STREAM_TOKENS) printf '%s\n' 16 ;;
    hardware:PRESSURE_TELEMETRY) printf '%s\n' auto ;;
    hardware:PRESSURE_GATE_MODE) printf '%s\n' warn ;;
    hardware:PRESSURE_MAX_MODELS_TOTAL_P95_DELTA_MS) printf '%s\n' 100 ;;
    hardware:PRESSURE_MAX_CHAT_TOTAL_P95_DELTA_MS) printf '%s\n' 500 ;;
    hardware:PRESSURE_MAX_STREAM_TTFB_P95_DELTA_MS) printf '%s\n' 250 ;;
    hardware:PRESSURE_MAX_DECISION_P95_MS) printf '%s\n' 100 ;;

    *)
      printf '%s\n' "$fallback"
      ;;
  esac
}
