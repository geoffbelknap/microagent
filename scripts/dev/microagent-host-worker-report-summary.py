#!/usr/bin/env python3
"""Summarize microagent host-worker probe reports.

The host-worker probe is intentionally runner-neutral. This helper keeps that
same boundary: it only flattens measured report fields so runs can be compared
without teaching microagent about runner scheduling or GPU execution internals.
"""

from __future__ import annotations

import argparse
import csv
import json
import sys
from pathlib import Path
from typing import Any


FIELDS = (
    "report",
    "backend",
    "engine",
    "workspace_count",
    "per_workspace_concurrency",
    "effective_concurrency",
    "endpoint",
    "host_median_ms",
    "guest_median_ms",
    "delta_ms",
    "guest_to_host_ratio",
    "host_p95_ms",
    "guest_p95_ms",
    "p95_delta_ms",
    "host_ttfb_median_ms",
    "guest_ttfb_median_ms",
    "ttfb_delta_ms",
    "pressure_runner",
    "pressure_gpu",
    "pressure_summary",
    "active_slot_fraction_median",
    "runner_active_median",
    "runner_active_max",
    "runner_slot_median",
    "runner_waiting_max",
    "runner_deferred_max",
    "runner_kv_cache_usage_median",
    "gpu_util_median",
    "gpu_util_p95",
    "gpu_util_max",
    "gpu_power_median_w",
    "gpu_power_p95_w",
    "gpu_power_max_w",
)


def get(doc: dict[str, Any] | None, *path: str) -> Any:
    value: Any = doc
    for part in path:
        if not isinstance(value, dict):
            return None
        value = value.get(part)
    return value


def stat(doc: dict[str, Any] | None, name: str) -> Any:
    if not isinstance(doc, dict):
        return None
    return doc.get(name)


def rows_for_report(path: Path) -> list[dict[str, Any]]:
    with path.open(encoding="utf-8") as f:
        report = json.load(f)

    rows: list[dict[str, Any]] = []
    levels = report.get("concurrency_levels") or []
    matrix = report.get("matrix") or {}
    pressure = get(report, "pressure", "levels") or {}
    runner = report.get("runner") or {}

    for level in levels:
        level_key = str(level)
        level_doc = matrix.get(level_key) or {}
        pressure_doc = pressure.get(level_key) or {}
        classification = pressure_doc.get("classification") or {}
        runner_pressure = pressure_doc.get("runner") or {}
        gpu_pressure = pressure_doc.get("gpu") or {}

        active = runner_pressure.get("active_requests") or {}
        slots = runner_pressure.get("slot_count") or {}
        waiting = runner_pressure.get("waiting_requests") or {}
        deferred = runner_pressure.get("deferred_requests") or {}
        kv_usage = runner_pressure.get("kv_cache_usage") or {}
        gpu_util = gpu_pressure.get("gpu_util_pct") or {}
        gpu_power = gpu_pressure.get("power_draw_w") or {}

        endpoints = sorted((level_doc.get("guest") or {}).keys())
        for endpoint in endpoints:
            host = get(level_doc, "host", endpoint) or {}
            guest = get(level_doc, "guest", endpoint) or {}
            overhead = get(level_doc, "overhead", endpoint) or {}
            rows.append(
                {
                    "report": str(path),
                    "backend": report.get("backend"),
                    "engine": runner.get("engine"),
                    "workspace_count": report.get("workspace_count"),
                    "per_workspace_concurrency": level,
                    "effective_concurrency": guest.get("concurrency")
                    or pressure_doc.get("effective_concurrency"),
                    "endpoint": endpoint,
                    "host_median_ms": host.get("median_ms"),
                    "guest_median_ms": guest.get("median_ms"),
                    "delta_ms": overhead.get("delta_ms"),
                    "guest_to_host_ratio": overhead.get("guest_to_host_ratio"),
                    "host_p95_ms": host.get("p95_ms"),
                    "guest_p95_ms": guest.get("p95_ms"),
                    "p95_delta_ms": overhead.get("p95_delta_ms"),
                    "host_ttfb_median_ms": host.get("ttfb_median_ms"),
                    "guest_ttfb_median_ms": guest.get("ttfb_median_ms"),
                    "ttfb_delta_ms": overhead.get("ttfb_delta_ms"),
                    "pressure_runner": classification.get("runner"),
                    "pressure_gpu": classification.get("gpu"),
                    "pressure_summary": classification.get("summary"),
                    "active_slot_fraction_median": runner_pressure.get(
                        "active_slot_fraction_median"
                    ),
                    "runner_active_median": stat(active, "median"),
                    "runner_active_max": stat(active, "max"),
                    "runner_slot_median": stat(slots, "median"),
                    "runner_waiting_max": stat(waiting, "max"),
                    "runner_deferred_max": stat(deferred, "max"),
                    "runner_kv_cache_usage_median": stat(kv_usage, "median"),
                    "gpu_util_median": stat(gpu_util, "median"),
                    "gpu_util_p95": stat(gpu_util, "p95"),
                    "gpu_util_max": stat(gpu_util, "max"),
                    "gpu_power_median_w": stat(gpu_power, "median"),
                    "gpu_power_p95_w": stat(gpu_power, "p95"),
                    "gpu_power_max_w": stat(gpu_power, "max"),
                }
            )
    return rows


def write_tsv(rows: list[dict[str, Any]]) -> None:
    writer = csv.DictWriter(
        sys.stdout,
        fieldnames=FIELDS,
        delimiter="\t",
        extrasaction="ignore",
        lineterminator="\n",
    )
    writer.writeheader()
    writer.writerows(rows)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--json",
        action="store_true",
        help="write a JSON array instead of tab-separated rows",
    )
    parser.add_argument("reports", nargs="+", type=Path)
    args = parser.parse_args()

    rows: list[dict[str, Any]] = []
    for report in args.reports:
        rows.extend(rows_for_report(report))

    if args.json:
        json.dump(rows, sys.stdout, indent=2, sort_keys=True)
        sys.stdout.write("\n")
    else:
        write_tsv(rows)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
