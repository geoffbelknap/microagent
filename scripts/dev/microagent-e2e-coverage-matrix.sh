#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/dev/e2e-lib.sh disable=SC1091
. "$ROOT/scripts/dev/e2e-lib.sh"

STATE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/microagent-e2e-coverage-matrix.XXXXXX")"
KEEP_VAR="${MICROAGENT_KEEP_MICROAGENT_E2E_COVERAGE_MATRIX:-0}"

cleanup() {
  status="$?"
  chmod -R u+w "$STATE_DIR" 2>/dev/null || true
  if [ "$status" -eq 0 ] && [ "$KEEP_VAR" != "1" ]; then
    rm -rf "$STATE_DIR"
  else
    echo "kept microagent E2E coverage matrix state at $STATE_DIR" >&2
  fi
}
trap cleanup EXIT

for required in go python3; do
  e2e_require_cmd "$required" "$required is required for microagent coverage matrix E2E"
done

export GOCACHE="${GOCACHE:-$STATE_DIR/gocache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/gomodcache}"
export GOFLAGS="${GOFLAGS:-} -modcacherw"

CLI="$STATE_DIR/microagent"
(cd "$ROOT" && go build -buildvcs=false -o "$CLI" ./cmd/microagent)

MICROAGENT_E2E_LIST_TSV=1 "$ROOT/scripts/dev/microagent-e2e.sh" --list >"$STATE_DIR/list.txt"
MICROAGENT_E2E_MATRIX_TSV=1 "$ROOT/scripts/dev/microagent-e2e.sh" --matrix >"$STATE_DIR/matrix.txt"
"$CLI" help >"$STATE_DIR/help.txt"

python3 - "$STATE_DIR/list.txt" "$STATE_DIR/matrix.txt" "$STATE_DIR/help.txt" <<'PY'
import re
import sys
from pathlib import Path

list_path, matrix_path, help_path = map(Path, sys.argv[1:4])
list_text = list_path.read_text(encoding="utf-8")
matrix_text = matrix_path.read_text(encoding="utf-8")
help_text = help_path.read_text(encoding="utf-8")

for header in ("SCENARIO", "COVERAGE", "BACKENDS", "FEATURES"):
    if header not in list_text.splitlines()[0]:
        raise SystemExit(f"--list header missing {header}")

for header in ("FEATURE", "CLASS", "REQUIRED_BACKENDS", "SCENARIOS", "NOTES"):
    if header not in matrix_text.splitlines()[0]:
        raise SystemExit(f"--matrix header missing {header}")

scenarios = set()
for line in list_text.splitlines()[1:]:
    if not line.strip():
        continue
    scenarios.add(line.split("\t", 1)[0])

matrix_rows = []
for line in matrix_text.splitlines()[1:]:
    if not line.strip():
        continue
    parts = line.split("\t")
    if len(parts) != 5:
        raise SystemExit(f"matrix row is not column-shaped: {line!r}")
    matrix_rows.append(
        {
            "feature": parts[0],
            "class": parts[1],
            "backends": parts[2],
            "scenarios": [s for s in parts[3].split(",") if s],
            "notes": parts[4],
        }
    )

if not matrix_rows:
    raise SystemExit("matrix has no rows")

valid_classes = {"portable", "backend-neutral", "backend-specific", "host-specific"}
for row in matrix_rows:
    if row["class"] not in valid_classes:
        raise SystemExit(f"unknown matrix class for {row['feature']}: {row['class']}")
    for scenario in row["scenarios"]:
        if scenario not in scenarios:
            raise SystemExit(f"matrix feature {row['feature']} references unknown scenario {scenario}")

required_features = {
    "help/version",
    "init",
    "contract",
    "profiles",
    "host/doctor",
    "kernel install/verify",
    "rootfs build",
    "run/create/start",
    "status/list",
    "result/logs/artifact",
    "events/stats",
    "connect",
    "exec",
    "halt/quarantine/stop/kill/delete",
    "clone/cp",
    "apply",
    "network status/modes/publish",
    "volume create/list/status/delete",
    "commit/image",
    "registry auth",
    "secrets",
    "health",
    "supervise",
    "snapshot/pause/resume",
    "model",
    "perf",
    "serve mcp",
    "serve mcp lifecycle",
    "AX/text output",
}

compact_help_commands = {
    "run",
    "create",
    "start",
    "exec",
    "connect",
    "status",
    "list",
    "ps",
    "logs",
    "halt",
    "delete",
    "doctor",
    "image",
    "volume",
    "network",
    "model",
    "artifact",
    "secret check",
}

matrix_features = {row["feature"] for row in matrix_rows}
missing_rows = sorted(required_features - matrix_features)
if missing_rows:
    raise SystemExit(f"matrix missing feature rows: {missing_rows}")

for command in sorted(compact_help_commands):
    pattern = r"(^|\n)\s+" + re.escape(command) + r"(\s|,|$)"
    if not re.search(pattern, help_text):
        raise SystemExit(f"CLI compact help no longer exposes {command!r}; update matrix expectation")

scenario_classes = {}
for line in list_text.splitlines()[1:]:
    if not line.strip():
        continue
    parts = line.split("\t")
    if len(parts) < 6:
        raise SystemExit(f"scenario list row is not column-shaped: {line!r}")
    scenario_classes[parts[0]] = parts[3]

for scenario, coverage in scenario_classes.items():
    if coverage == "unknown":
        raise SystemExit(f"scenario {scenario} has no coverage metadata")

print("coverage matrix metadata ok")
PY

echo "microagent E2E coverage matrix passed"
