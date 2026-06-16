#!/usr/bin/env python3
"""Summarize runner-neutral model mediation pressure artifacts."""

from __future__ import annotations

import argparse
import csv
import json
import math
import sys
from collections import Counter, defaultdict
from pathlib import Path
from typing import Any


ROW_FIELDS = (
    "artifact",
    "status",
    "level",
    "case",
    "gate_status",
    "models_total_p95_delta_ms",
    "chat_total_p95_delta_ms",
    "stream_ttfb_p95_delta_ms",
    "decision_p95_ms",
    "runner_state",
    "gpu_state",
    "active_median",
    "waiting_max",
    "deferred_max",
    "gpu_util_median",
    "gpu_util_p95",
    "telemetry_summary",
)


def read_tsv(path: Path) -> list[dict[str, str]]:
    if not path.exists():
        return []
    with path.open(encoding="utf-8", newline="") as handle:
        return list(csv.DictReader(handle, delimiter="\t"))


def as_float(value: Any) -> float | None:
    if value is None or value == "":
        return None
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def fmt(value: Any) -> str:
    if value is None:
        return ""
    if isinstance(value, float):
        if math.isnan(value) or math.isinf(value):
            return ""
        return f"{value:.3f}".rstrip("0").rstrip(".")
    return str(value)


def clamp_delta(value: float | None) -> float | None:
    if value is None:
        return None
    return max(0.0, value)


def sort_level(value: str) -> tuple[int, str]:
    try:
        return (int(value), "")
    except (TypeError, ValueError):
        return (0, str(value))


def status_from_counts(counts: Counter[str]) -> str:
    if counts.get("fail", 0):
        return "fail"
    if counts.get("missing", 0):
        return "missing"
    if counts.get("pass", 0):
        return "pass"
    return "unknown"


def load_artifact(artifact: Path) -> dict[str, list[dict[str, str]]]:
    return {
        "comparison": read_tsv(artifact / "pressure-profile-comparison.tsv"),
        "audit": read_tsv(artifact / "pressure-audit-summary.tsv"),
        "telemetry": read_tsv(artifact / "pressure-telemetry-summary.tsv"),
        "gates": read_tsv(artifact / "pressure-gates.tsv"),
    }


def comparison_map(rows: list[dict[str, str]]) -> dict[tuple[str, str, str], dict[str, str]]:
    mapped: dict[tuple[str, str, str], dict[str, str]] = {}
    for row in rows:
        mapped[(row.get("level", ""), row.get("case", ""), row.get("endpoint", ""))] = row
    return mapped


def audit_map(rows: list[dict[str, str]]) -> dict[tuple[str, str], dict[str, str]]:
    return {(row.get("level", ""), row.get("case", "")): row for row in rows}


def telemetry_map(rows: list[dict[str, str]]) -> dict[tuple[str, str], dict[str, str]]:
    mapped: dict[tuple[str, str], dict[str, str]] = {}
    for row in rows:
        phase = row.get("phase", "")
        if ":c=" not in phase:
            continue
        case, level = phase.split(":c=", 1)
        mapped[(level, case)] = row
    return mapped


def gate_counts_by_scope(rows: list[dict[str, str]]) -> dict[tuple[str, str], Counter[str]]:
    counts: dict[tuple[str, str], Counter[str]] = defaultdict(Counter)
    for row in rows:
        counts[(row.get("level", ""), row.get("case", ""))][row.get("status", "unknown")] += 1
    return counts


def collect_levels_cases(rows: list[dict[str, str]]) -> list[tuple[str, str]]:
    pairs = {
        (row.get("level", ""), row.get("case", ""))
        for row in rows
        if row.get("case") and row.get("case") != "direct"
    }
    return sorted(pairs, key=lambda item: (sort_level(item[0]), item[1]))


def build_rows(artifact: Path, data: dict[str, list[dict[str, str]]]) -> list[dict[str, str]]:
    comparisons = comparison_map(data["comparison"])
    audits = audit_map(data["audit"])
    telemetry = telemetry_map(data["telemetry"])
    gates = gate_counts_by_scope(data["gates"])
    rows: list[dict[str, str]] = []
    for level, case in collect_levels_cases(data["comparison"]):
        models = comparisons.get((level, case, "models"), {})
        chat = comparisons.get((level, case, "chat"), {})
        stream = comparisons.get((level, case, "stream"), {})
        audit = audits.get((level, case), {})
        telem = telemetry.get((level, case), {})
        gate_status = status_from_counts(gates.get((level, case), Counter()))
        rows.append(
            {
                "artifact": str(artifact),
                "status": gate_status,
                "level": level,
                "case": case,
                "gate_status": gate_status,
                "models_total_p95_delta_ms": fmt(clamp_delta(as_float(models.get("delta_total_p95_ms")))),
                "chat_total_p95_delta_ms": fmt(clamp_delta(as_float(chat.get("delta_total_p95_ms")))),
                "stream_ttfb_p95_delta_ms": fmt(clamp_delta(as_float(stream.get("delta_ttfb_p95_ms")))),
                "decision_p95_ms": fmt(as_float(audit.get("decision_p95_ms"))),
                "runner_state": telem.get("runner_state", ""),
                "gpu_state": telem.get("gpu_state", ""),
                "active_median": telem.get("active_median", ""),
                "waiting_max": telem.get("waiting_max", ""),
                "deferred_max": telem.get("deferred_max", ""),
                "gpu_util_median": telem.get("gpu_util_median", ""),
                "gpu_util_p95": telem.get("gpu_util_p95", ""),
                "telemetry_summary": telem.get("summary", ""),
            }
        )
    return rows


def max_metric(rows: list[dict[str, str]], field: str) -> dict[str, Any]:
    best: dict[str, Any] = {"value": None, "level": "", "case": ""}
    for row in rows:
        value = as_float(row.get(field))
        if value is None:
            continue
        if best["value"] is None or value > best["value"]:
            best = {"value": value, "level": row.get("level", ""), "case": row.get("case", "")}
    return best


def gate_status_counts(rows: list[dict[str, str]]) -> Counter[str]:
    counts: Counter[str] = Counter()
    for row in rows:
        counts[row.get("status", "unknown")] += 1
    return counts


def worst_gates(rows: list[dict[str, str]], limit: int = 5) -> list[dict[str, str]]:
    ranked = []
    for row in rows:
        actual = as_float(row.get("actual"))
        budget = as_float(row.get("limit"))
        if actual is None or budget in (None, 0):
            ratio = None
        else:
            ratio = actual / budget
        ranked.append((row.get("status") != "pass", ratio if ratio is not None else -1, row))
    ranked.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return [row for _failed, _ratio, row in ranked[:limit]]


def build_summary(artifact: Path) -> tuple[dict[str, Any], list[dict[str, str]]]:
    data = load_artifact(artifact)
    missing = [
        name
        for name, path in (
            ("pressure-profile-comparison.tsv", artifact / "pressure-profile-comparison.tsv"),
            ("pressure-audit-summary.tsv", artifact / "pressure-audit-summary.tsv"),
            ("pressure-gates.tsv", artifact / "pressure-gates.tsv"),
        )
        if not path.exists()
    ]
    rows = build_rows(artifact, data)
    gate_counts = gate_status_counts(data["gates"])
    row_counts = gate_status_counts(rows)
    telemetry_rows = data["telemetry"]
    runner_states = sorted({row.get("runner_state", "") for row in telemetry_rows if row.get("runner_state")})
    gpu_states = sorted({row.get("gpu_state", "") for row in telemetry_rows if row.get("gpu_state")})
    telemetry_summaries = sorted({row.get("summary", "") for row in telemetry_rows if row.get("summary")})
    status = "missing" if missing else status_from_counts(gate_counts)
    maxes = {
        "models_total_p95_delta_ms": max_metric(rows, "models_total_p95_delta_ms"),
        "chat_total_p95_delta_ms": max_metric(rows, "chat_total_p95_delta_ms"),
        "stream_ttfb_p95_delta_ms": max_metric(rows, "stream_ttfb_p95_delta_ms"),
        "decision_p95_ms": max_metric(rows, "decision_p95_ms"),
    }
    notes = [
        "positive direct-vs-mediated deltas are reported; negative deltas are clamped to zero for the decision read",
        "pressure gates still decide pass/fail against the artifact's configured limits",
        "telemetry is advisory and depends on runner/GPU sampling availability",
    ]
    if not telemetry_rows:
        notes.append("no pressure-telemetry-summary.tsv was present")
    summary = {
        "schema_version": 1,
        "artifact": str(artifact),
        "status": status,
        "missing_inputs": missing,
        "gate_status_counts": dict(sorted(gate_counts.items())),
        "row_status_counts": dict(sorted(row_counts.items())),
        "max_positive_metrics": maxes,
        "telemetry": {
            "runner_states": runner_states,
            "gpu_states": gpu_states,
            "summaries": telemetry_summaries,
            "sampled": bool(telemetry_rows),
        },
        "worst_gates": worst_gates(data["gates"]),
        "rows": rows,
        "notes": notes,
    }
    return summary, rows


def write_json(path: Path, summary: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def write_tsv(path: Path, rows: list[dict[str, str]]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=ROW_FIELDS, delimiter="\t", lineterminator="\n")
        writer.writeheader()
        writer.writerows(rows)


def compact_metric(summary: dict[str, Any], key: str) -> str:
    item = summary["max_positive_metrics"][key]
    value = item.get("value")
    if value is None:
        return "n/a"
    return f"{fmt(value)}ms ({item.get('case')} c={item.get('level')})"


def render_text(summary: dict[str, Any], rows: list[dict[str, str]]) -> str:
    lines = [
        f"pressure decision: {summary['status']} {summary['artifact']}",
        "gates: "
        + ", ".join(f"{key}={value}" for key, value in summary["gate_status_counts"].items())
        if summary["gate_status_counts"]
        else "gates: none",
        "max positive deltas: "
        f"models={compact_metric(summary, 'models_total_p95_delta_ms')} "
        f"chat={compact_metric(summary, 'chat_total_p95_delta_ms')} "
        f"stream_ttfb={compact_metric(summary, 'stream_ttfb_p95_delta_ms')} "
        f"decision={compact_metric(summary, 'decision_p95_ms')}",
    ]
    telemetry = summary["telemetry"]
    if telemetry["sampled"]:
        lines.append(
            "telemetry: "
            f"runner={','.join(telemetry['runner_states']) or 'n/a'} "
            f"gpu={','.join(telemetry['gpu_states']) or 'n/a'}"
        )
        for item in telemetry["summaries"][:3]:
            lines.append(f"telemetry read: {item}")
    else:
        lines.append("telemetry: not sampled")
    if summary["missing_inputs"]:
        lines.append("missing inputs: " + ", ".join(summary["missing_inputs"]))
    lines.append("rows:")
    for row in rows:
        lines.append(
            "  "
            f"c={row['level']} {row['case']} {row['gate_status']} "
            f"models={row['models_total_p95_delta_ms'] or 'n/a'}ms "
            f"chat={row['chat_total_p95_delta_ms'] or 'n/a'}ms "
            f"stream_ttfb={row['stream_ttfb_p95_delta_ms'] or 'n/a'}ms "
            f"decision={row['decision_p95_ms'] or 'n/a'}ms"
        )
    return "\n".join(lines) + "\n"


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("artifact_dir", type=Path, help="pressure artifact directory")
    parser.add_argument("--out-json", type=Path, help="write machine-readable summary JSON")
    parser.add_argument("--out-tsv", type=Path, help="write row-level compact TSV")
    parser.add_argument("--format", choices=("text", "json", "tsv"), default="text")
    parser.add_argument("--check", action="store_true", help="exit non-zero unless status is pass")
    return parser


def main() -> int:
    args = build_parser().parse_args()
    artifact = args.artifact_dir.resolve()
    summary, rows = build_summary(artifact)
    if args.out_json:
        write_json(args.out_json, summary)
    if args.out_tsv:
        write_tsv(args.out_tsv, rows)
    if args.format == "json":
        print(json.dumps(summary, indent=2, sort_keys=True))
    elif args.format == "tsv":
        writer = csv.DictWriter(sys.stdout, fieldnames=ROW_FIELDS, delimiter="\t", lineterminator="\n")
        writer.writeheader()
        writer.writerows(rows)
    else:
        print(render_text(summary, rows), end="")
    return 1 if args.check and summary["status"] != "pass" else 0


if __name__ == "__main__":
    raise SystemExit(main())
