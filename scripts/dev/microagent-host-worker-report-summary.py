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
    "worker_protocol",
    "runner_engine",
    "runner_version",
    "launch_mode",
    "telemetry_adapter",
    "diagnostic_sources",
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
    "worker_protocol",
    "runner_engine",
    "runner_version",
    "launch_mode",
    "telemetry_adapter",
    "diagnostic_sources",
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

COMPACT_PRESSURE_FIELDS = (
    "workload",
    "run_label",
    "worker_protocol",
    "runner_engine",
    "runner_version",
    "launch_mode",
    "telemetry_adapter",
    "runner_slots",
    "workspace_count",
    "per_workspace_concurrency",
    "effective_concurrency",
    "pressure_runner",
    "pressure_gpu",
    "active_slot_fraction_median",
    "runner_active_median",
    "runner_slot_median",
    "gpu_util_median",
    "gpu_util_p95",
    "chat_delta_ms",
    "chat_p95_delta_ms",
    "stream_delta_ms",
    "stream_p95_delta_ms",
    "stream_ttfb_delta_ms",
    "pressure_summary",
)

RUNNER_PROFILE_FIELDS = (
    "report",
    "run_label",
    "worker_protocol",
    "runner_engine",
    "runner_version",
    "launch_mode",
    "telemetry_adapter",
    "diagnostic_sources",
    "conformance_required_ok",
    "conformance_models",
    "conformance_streaming",
    "runner_slots",
    "workspace_count",
    "per_workspace_concurrency",
    "effective_concurrency",
    "pressure_runner",
    "pressure_gpu",
    "active_slot_fraction_median",
    "runner_active_median",
    "runner_slot_median",
    "runner_waiting_max",
    "runner_deferred_max",
    "runner_kv_cache_usage_median",
    "gpu_util_median",
    "gpu_util_p95",
    "gpu_power_median_w",
    "models_guest_median_ms",
    "models_delta_ms",
    "chat_guest_median_ms",
    "chat_delta_ms",
    "chat_p95_delta_ms",
    "chat_ttfb_delta_ms",
    "stream_guest_median_ms",
    "stream_delta_ms",
    "stream_p95_delta_ms",
    "stream_ttfb_delta_ms",
    "stream_chunk_gap_delta_ms",
    "pressure_summary",
    "profile_brief",
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


def joined(values: Any) -> str | None:
    if not values:
        return None
    if isinstance(values, list):
        return ",".join(str(value) for value in values)
    return str(values)


def host_worker_meta(report: dict[str, Any]) -> dict[str, Any]:
    host_worker = report.get("host_worker") or {}
    runner = report.get("runner") or {}
    diagnostics = host_worker.get("diagnostics") or {}
    telemetry = report.get("telemetry") or {}
    runner_telemetry = telemetry.get("runner") or {}
    pressure = report.get("pressure") or {}

    runner_engine = host_worker.get("runner_engine") or runner.get("engine")
    launch_mode = host_worker.get("launch_mode") or runner.get("mode")
    telemetry_adapter = (
        diagnostics.get("runner_adapter")
        or pressure.get("runner_adapter")
        or runner_telemetry.get("adapter")
    )
    diagnostic_sources = (
        diagnostics.get("sources")
        or pressure.get("runner_diagnostic_sources")
        or runner_telemetry.get("diagnostic_sources")
    )
    return {
        "worker_protocol": host_worker.get("protocol"),
        "runner_engine": runner_engine,
        "runner_version": host_worker.get("runner_version") or runner.get("version"),
        "launch_mode": launch_mode,
        "telemetry_adapter": telemetry_adapter,
        "diagnostic_sources": joined(diagnostic_sources),
    }


def conformance_meta(report: dict[str, Any]) -> dict[str, Any]:
    host_worker = report.get("host_worker") or {}
    conformance = host_worker.get("conformance") or {}
    capabilities = conformance.get("capabilities") or {}
    return {
        "conformance_required_ok": capabilities.get("required_ok"),
        "conformance_models": capabilities.get("model_count"),
        "conformance_streaming": capabilities.get("streaming_chat_completions"),
    }


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
    worker_meta = host_worker_meta(report)

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
                    "engine": worker_meta.get("runner_engine") or runner.get("engine"),
                    "runner_mode": worker_meta.get("launch_mode") or runner.get("mode"),
                    **worker_meta,
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


def format_value(item: Any, suffix: str = "") -> str:
    if item is None or item == "":
        return "na"
    if isinstance(item, bool):
        text = "true" if item else "false"
    elif isinstance(item, float):
        text = f"{item:.3f}".rstrip("0").rstrip(".")
    else:
        text = str(item)
    return f"{text}{suffix}"


def pressure_rows_for_report(path: Path) -> list[dict[str, Any]]:
    with path.open(encoding="utf-8") as f:
        report = json.load(f)

    rows: list[dict[str, Any]] = []
    matrix = report.get("matrix") or {}
    pressure = get(report, "pressure", "levels") or {}
    experiment = report.get("experiment") or {}
    worker_meta = host_worker_meta(report)

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
            **worker_meta,
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


def profile_brief(row: dict[str, Any]) -> str:
    workload = (
        f"ws={format_value(row.get('workspace_count'))} "
        f"c={format_value(row.get('per_workspace_concurrency'))} "
        f"total={format_value(row.get('effective_concurrency'))}"
    )
    conformance = (
        f"required={format_value(row.get('conformance_required_ok'))} "
        f"models={format_value(row.get('conformance_models'))} "
        f"streaming={format_value(row.get('conformance_streaming'))}"
    )
    runner = (
        f"{row.get('runner_engine') or 'unknown'} "
        f"protocol={row.get('worker_protocol') or 'unknown'} "
        f"adapter={row.get('telemetry_adapter') or 'none'}"
    )
    pressure = (
        f"runner={row.get('pressure_runner') or 'unavailable'} "
        f"active={format_value(row.get('runner_active_median'))}/"
        f"{format_value(row.get('runner_slot_median'))} "
        f"wait_max={format_value(row.get('runner_waiting_max'))} "
        f"def_max={format_value(row.get('runner_deferred_max'))}"
    )
    gpu = (
        f"gpu={row.get('pressure_gpu') or 'unavailable'} "
        f"util={format_value(row.get('gpu_util_median'), '%')}/"
        f"{format_value(row.get('gpu_util_p95'), '%')} "
        f"power={format_value(row.get('gpu_power_median_w'), 'W')}"
    )
    latency = (
        f"models={format_value(row.get('models_delta_ms'), 'ms')} "
        f"chat={format_value(row.get('chat_delta_ms'), 'ms')} "
        f"stream={format_value(row.get('stream_delta_ms'), 'ms')} "
        f"stream_ttfb={format_value(row.get('stream_ttfb_delta_ms'), 'ms')}"
    )
    return (
        f"{runner}; {workload}; {conformance}; {pressure}; {gpu}; "
        f"delta {latency}; {row.get('pressure_summary') or 'no pressure summary'}"
    )


def runner_profile_rows_for_report(path: Path) -> list[dict[str, Any]]:
    with path.open(encoding="utf-8") as f:
        report = json.load(f)

    rows: list[dict[str, Any]] = []
    matrix = report.get("matrix") or {}
    pressure = get(report, "pressure", "levels") or {}
    experiment = report.get("experiment") or {}
    worker_meta = host_worker_meta(report)
    conformance = conformance_meta(report)

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
        guest = level_doc.get("guest") or {}
        overhead = level_doc.get("overhead") or {}
        models_guest = guest.get("models") or {}
        chat_guest = guest.get("chat") or {}
        stream_guest = guest.get("stream") or {}
        models = overhead.get("models") or {}
        chat = overhead.get("chat") or {}
        stream = overhead.get("stream") or {}

        row = {
            "report": str(path),
            "run_label": experiment.get("label"),
            **worker_meta,
            **conformance,
            "runner_slots": experiment.get("runner_slots"),
            "workspace_count": report.get("workspace_count"),
            "per_workspace_concurrency": level,
            "effective_concurrency": pressure_doc.get("effective_concurrency")
            or chat_guest.get("concurrency")
            or stream_guest.get("concurrency")
            or models_guest.get("concurrency"),
            "pressure_runner": classification.get("runner"),
            "pressure_gpu": classification.get("gpu"),
            "active_slot_fraction_median": runner_pressure.get(
                "active_slot_fraction_median"
            ),
            "runner_active_median": stat(active, "median"),
            "runner_slot_median": stat(slots, "median"),
            "runner_waiting_max": stat(waiting, "max"),
            "runner_deferred_max": stat(deferred, "max"),
            "runner_kv_cache_usage_median": stat(kv_usage, "median"),
            "gpu_util_median": stat(gpu_util, "median"),
            "gpu_util_p95": stat(gpu_util, "p95"),
            "gpu_power_median_w": stat(gpu_power, "median"),
            "models_guest_median_ms": models_guest.get("median_ms"),
            "models_delta_ms": models.get("delta_ms"),
            "chat_guest_median_ms": chat_guest.get("median_ms"),
            "chat_delta_ms": chat.get("delta_ms"),
            "chat_p95_delta_ms": chat.get("p95_delta_ms"),
            "chat_ttfb_delta_ms": chat.get("ttfb_delta_ms"),
            "stream_guest_median_ms": stream_guest.get("median_ms"),
            "stream_delta_ms": stream.get("delta_ms"),
            "stream_p95_delta_ms": stream.get("p95_delta_ms"),
            "stream_ttfb_delta_ms": stream.get("ttfb_delta_ms"),
            "stream_chunk_gap_delta_ms": stream.get(
                "body_read_per_chunk_gap_delta_ms"
            ),
            "pressure_summary": classification.get("summary"),
        }
        row["profile_brief"] = profile_brief(row)
        rows.append(row)
    return rows


def compact_pressure_rows(rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
    compact: list[dict[str, Any]] = []
    for row in rows:
        next_row = dict(row)
        next_row["workload"] = (
            f"ws={row.get('workspace_count')} "
            f"c={row.get('per_workspace_concurrency')} "
            f"total={row.get('effective_concurrency')}"
        )
        compact.append(next_row)
    return sorted(
        compact,
        key=lambda row: (
            row.get("workspace_count") or 0,
            row.get("per_workspace_concurrency") or 0,
            row.get("effective_concurrency") or 0,
            row.get("runner_slots") or 0,
            row.get("run_label") or "",
        ),
    )


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
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument(
        "--pressure",
        action="store_true",
        help="write one pressure/backpressure row per report and concurrency level",
    )
    mode.add_argument(
        "--profiles",
        action="store_true",
        help="write one compact runner profile row per report and concurrency level",
    )
    parser.add_argument(
        "--compact",
        action="store_true",
        help="with --pressure, write a smaller comparison view sorted by workload",
    )
    parser.add_argument("reports", nargs="+", type=Path)
    args = parser.parse_args()

    rows: list[dict[str, Any]] = []
    for report in args.reports:
        if args.profiles:
            rows.extend(runner_profile_rows_for_report(report))
        elif args.pressure:
            rows.extend(pressure_rows_for_report(report))
        else:
            rows.extend(rows_for_report(report))
    if args.compact:
        if not args.pressure:
            parser.error("--compact requires --pressure")
        rows = compact_pressure_rows(rows)

    if args.json:
        json.dump(rows, sys.stdout, indent=2, sort_keys=True)
        sys.stdout.write("\n")
    else:
        fields = FIELDS
        if args.profiles:
            fields = RUNNER_PROFILE_FIELDS
        elif args.pressure:
            fields = COMPACT_PRESSURE_FIELDS if args.compact else PRESSURE_FIELDS
        write_tsv(rows, fields)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
