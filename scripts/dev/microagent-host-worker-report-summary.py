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
    "runner_mode",
    "run_label",
    "runner_slots",
    "host_baseline",
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
    "host_body_read_median_ms",
    "guest_body_read_median_ms",
    "body_read_delta_ms",
    "host_body_read_per_chunk_median_ms",
    "guest_body_read_per_chunk_median_ms",
    "body_read_per_chunk_delta_ms",
    "host_body_read_per_chunk_gap_median_ms",
    "guest_body_read_per_chunk_gap_median_ms",
    "body_read_per_chunk_gap_delta_ms",
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

PRESSURE_FIELDS = (
    "report",
    "run_label",
    "runner_slots",
    "workspace_count",
    "per_workspace_concurrency",
    "effective_concurrency",
    "pressure_runner",
    "pressure_gpu",
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
    "chat_delta_ms",
    "chat_p95_delta_ms",
    "chat_ttfb_delta_ms",
    "stream_delta_ms",
    "stream_p95_delta_ms",
    "stream_ttfb_delta_ms",
    "stream_chunk_gap_delta_ms",
    "pressure_summary",
    "pressure_brief",
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
    experiment = report.get("experiment") or {}
    measurement_design = report.get("measurement_design") or {}

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
                    "runner_mode": runner.get("mode"),
                    "run_label": experiment.get("label"),
                    "runner_slots": experiment.get("runner_slots"),
                    "host_baseline": measurement_design.get("host_baseline"),
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
                    "host_body_read_median_ms": host.get("body_read_median_ms"),
                    "guest_body_read_median_ms": guest.get("body_read_median_ms"),
                    "body_read_delta_ms": overhead.get("body_read_delta_ms"),
                    "host_body_read_per_chunk_median_ms": host.get(
                        "body_read_per_chunk_median_ms"
                    ),
                    "guest_body_read_per_chunk_median_ms": guest.get(
                        "body_read_per_chunk_median_ms"
                    ),
                    "body_read_per_chunk_delta_ms": overhead.get(
                        "body_read_per_chunk_delta_ms"
                    ),
                    "host_body_read_per_chunk_gap_median_ms": host.get(
                        "body_read_per_chunk_gap_median_ms"
                    ),
                    "guest_body_read_per_chunk_gap_median_ms": guest.get(
                        "body_read_per_chunk_gap_median_ms"
                    ),
                    "body_read_per_chunk_gap_delta_ms": overhead.get(
                        "body_read_per_chunk_gap_delta_ms"
                    ),
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


def pressure_brief(row: dict[str, Any]) -> str:
    def value(item: Any, suffix: str = "") -> str:
        if item is None or item == "":
            return "na"
        if isinstance(item, float):
            text = f"{item:.3f}".rstrip("0").rstrip(".")
        else:
            text = str(item)
        return f"{text}{suffix}"

    active = value(row.get("runner_active_median"))
    slots = value(row.get("runner_slot_median"))
    fraction = value(row.get("active_slot_fraction_median"))
    waiting = value(row.get("runner_waiting_max"))
    deferred = value(row.get("runner_deferred_max"))
    gpu_median = value(row.get("gpu_util_median"), "%")
    gpu_p95 = value(row.get("gpu_util_p95"), "%")
    chat_delta = value(row.get("chat_delta_ms"), "ms")
    stream_delta = value(row.get("stream_delta_ms"), "ms")
    stream_ttfb = value(row.get("stream_ttfb_delta_ms"), "ms")
    summary = row.get("pressure_summary") or "pressure source unavailable"
    return (
        f"runner={row.get('pressure_runner') or 'unavailable'} "
        f"active={active}/{slots} frac={fraction} wait_max={waiting} def_max={deferred}; "
        f"gpu={row.get('pressure_gpu') or 'unavailable'} util={gpu_median}/{gpu_p95}; "
        f"chat_delta={chat_delta} stream_delta={stream_delta} stream_ttfb_delta={stream_ttfb}; "
        f"{summary}"
    )


def pressure_rows_for_report(path: Path) -> list[dict[str, Any]]:
    with path.open(encoding="utf-8") as f:
        report = json.load(f)

    rows: list[dict[str, Any]] = []
    matrix = report.get("matrix") or {}
    pressure = get(report, "pressure", "levels") or {}
    experiment = report.get("experiment") or {}

    for level in report.get("concurrency_levels") or []:
        level_key = str(level)
        pressure_doc = pressure.get(level_key) or {}
        classification = pressure_doc.get("classification") or {}
        runner_pressure = pressure_doc.get("runner") or {}
        gpu_pressure = pressure_doc.get("gpu") or {}
        level_doc = matrix.get(level_key) or {}

        active = runner_pressure.get("active_requests") or {}
        slots = runner_pressure.get("slot_count") or {}
        waiting = runner_pressure.get("waiting_requests") or {}
        deferred = runner_pressure.get("deferred_requests") or {}
        kv_usage = runner_pressure.get("kv_cache_usage") or {}
        gpu_util = gpu_pressure.get("gpu_util_pct") or {}
        gpu_power = gpu_pressure.get("power_draw_w") or {}
        chat = get(level_doc, "overhead", "chat") or {}
        stream = get(level_doc, "overhead", "stream") or {}

        row = {
            "report": str(path),
            "run_label": experiment.get("label"),
            "runner_slots": experiment.get("runner_slots"),
            "workspace_count": report.get("workspace_count"),
            "per_workspace_concurrency": level,
            "effective_concurrency": pressure_doc.get("effective_concurrency"),
            "pressure_runner": classification.get("runner"),
            "pressure_gpu": classification.get("gpu"),
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
            "chat_delta_ms": chat.get("delta_ms"),
            "chat_p95_delta_ms": chat.get("p95_delta_ms"),
            "chat_ttfb_delta_ms": chat.get("ttfb_delta_ms"),
            "stream_delta_ms": stream.get("delta_ms"),
            "stream_p95_delta_ms": stream.get("p95_delta_ms"),
            "stream_ttfb_delta_ms": stream.get("ttfb_delta_ms"),
            "stream_chunk_gap_delta_ms": stream.get(
                "body_read_per_chunk_gap_delta_ms"
            ),
            "pressure_summary": classification.get("summary"),
        }
        row["pressure_brief"] = pressure_brief(row)
        rows.append(row)
    return rows


def write_tsv(rows: list[dict[str, Any]], fields: tuple[str, ...]) -> None:
    writer = csv.DictWriter(
        sys.stdout,
        fieldnames=fields,
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
    parser.add_argument(
        "--pressure",
        action="store_true",
        help="write one pressure/backpressure row per report and concurrency level",
    )
    parser.add_argument("reports", nargs="+", type=Path)
    args = parser.parse_args()

    rows: list[dict[str, Any]] = []
    for report in args.reports:
        if args.pressure:
            rows.extend(pressure_rows_for_report(report))
        else:
            rows.extend(rows_for_report(report))

    if args.json:
        json.dump(rows, sys.stdout, indent=2, sort_keys=True)
        sys.stdout.write("\n")
    else:
        write_tsv(rows, PRESSURE_FIELDS if args.pressure else FIELDS)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
