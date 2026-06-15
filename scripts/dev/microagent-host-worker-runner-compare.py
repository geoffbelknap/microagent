#!/usr/bin/env python3
"""Compare runner-neutral host-worker probe artifacts.

This helper intentionally consumes only probe JSON reports and optional broker
diagnostic TSVs. It does not know how to start any runner, and it does not
encode runner-specific scheduling rules.
"""

from __future__ import annotations

import argparse
import csv
import json
import sys
from pathlib import Path
from typing import Any


FIELDS = (
    "run",
    "baseline_run",
    "runner_engine",
    "runner_version",
    "launch_mode",
    "telemetry_adapter",
    "worker_protocol",
    "conformance_required_ok",
    "conformance_streaming",
    "workload",
    "workspace_count",
    "per_workspace_concurrency",
    "effective_concurrency",
    "runner_slots",
    "pressure_runner",
    "pressure_gpu",
    "runner_active_median",
    "runner_slot_median",
    "runner_waiting_max",
    "runner_deferred_max",
    "runner_kv_cache_usage_median",
    "gpu_util_median",
    "gpu_util_vs_baseline",
    "gpu_util_p95",
    "gpu_power_median_w",
    "models_delta_ms",
    "chat_delta_ms",
    "chat_delta_vs_baseline_ms",
    "chat_p95_delta_ms",
    "chat_ttfb_delta_ms",
    "stream_delta_ms",
    "stream_delta_vs_baseline_ms",
    "stream_p95_delta_ms",
    "stream_ttfb_delta_ms",
    "stream_chunk_gap_delta_ms",
    "broker_models_guest_overhead_ms",
    "broker_chat_guest_overhead_ms",
    "broker_chat_guest_overhead_vs_baseline_ms",
    "broker_chat_guest_ttfb_overhead_ms",
    "broker_stream_guest_overhead_ms",
    "broker_stream_guest_ttfb_overhead_ms",
    "broker_stream_ttfb_vs_baseline_ms",
    "report",
    "broker_comparison",
)

BASELINE_FIELDS = (
    ("gpu_util_median", "gpu_util_vs_baseline"),
    ("chat_delta_ms", "chat_delta_vs_baseline_ms"),
    ("stream_delta_ms", "stream_delta_vs_baseline_ms"),
    ("broker_chat_guest_overhead_ms", "broker_chat_guest_overhead_vs_baseline_ms"),
    ("broker_stream_guest_ttfb_overhead_ms", "broker_stream_ttfb_vs_baseline_ms"),
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


def number(value: Any) -> float | None:
    if value is None or value == "":
        return None
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def diff(value: Any, baseline: Any) -> float | None:
    left = number(value)
    right = number(baseline)
    if left is None or right is None:
        return None
    return round(left - right, 3)


def parse_assignment(raw: str, flag: str) -> tuple[str, Path]:
    if "=" not in raw:
        raise argparse.ArgumentTypeError(f"{flag} must use LABEL=PATH")
    label, path = raw.split("=", 1)
    label = label.strip()
    if not label:
        raise argparse.ArgumentTypeError(f"{flag} label must not be empty")
    return label, Path(path)


def host_worker_meta(report: dict[str, Any]) -> dict[str, Any]:
    host_worker = report.get("host_worker") or {}
    runner = report.get("runner") or {}
    diagnostics = host_worker.get("diagnostics") or {}
    telemetry = report.get("telemetry") or {}
    runner_telemetry = telemetry.get("runner") or {}
    pressure = report.get("pressure") or {}
    conformance = host_worker.get("conformance") or {}
    capabilities = conformance.get("capabilities") or {}
    return {
        "worker_protocol": host_worker.get("protocol"),
        "runner_engine": host_worker.get("runner_engine") or runner.get("engine"),
        "runner_version": host_worker.get("runner_version") or runner.get("version"),
        "launch_mode": host_worker.get("launch_mode") or runner.get("mode"),
        "telemetry_adapter": (
            diagnostics.get("runner_adapter")
            or pressure.get("runner_adapter")
            or runner_telemetry.get("adapter")
        ),
        "conformance_required_ok": capabilities.get("required_ok"),
        "conformance_streaming": capabilities.get("streaming_chat_completions"),
    }


def broker_metrics(path: Path | None) -> dict[str, Any]:
    if path is None:
        return {}
    endpoint_rows: dict[str, dict[str, Any]] = {}
    with path.open(encoding="utf-8", newline="") as f:
        reader = csv.DictReader(f, delimiter="\t")
        for row in reader:
            endpoint = row.get("endpoint")
            if endpoint:
                endpoint_rows[endpoint] = row
    models = endpoint_rows.get("models") or {}
    chat = endpoint_rows.get("chat") or {}
    stream = endpoint_rows.get("stream") or {}
    return {
        "broker_comparison": str(path),
        "broker_models_guest_overhead_ms": models.get("guest_broker_overhead_ms"),
        "broker_chat_guest_overhead_ms": chat.get("guest_broker_overhead_ms"),
        "broker_chat_guest_ttfb_overhead_ms": chat.get(
            "guest_broker_ttfb_overhead_ms"
        ),
        "broker_stream_guest_overhead_ms": stream.get("guest_broker_overhead_ms"),
        "broker_stream_guest_ttfb_overhead_ms": stream.get(
            "guest_broker_ttfb_overhead_ms"
        ),
    }


def rows_for_report(
    label: str, report_path: Path, broker_path: Path | None
) -> list[dict[str, Any]]:
    with report_path.open(encoding="utf-8") as f:
        report = json.load(f)

    rows: list[dict[str, Any]] = []
    matrix = report.get("matrix") or {}
    pressure = get(report, "pressure", "levels") or {}
    experiment = report.get("experiment") or {}
    worker_meta = host_worker_meta(report)
    broker = broker_metrics(broker_path)

    for level in report.get("concurrency_levels") or []:
        level_key = str(level)
        pressure_doc = pressure.get(level_key) or {}
        classification = pressure_doc.get("classification") or {}
        runner_pressure = pressure_doc.get("runner") or {}
        gpu_pressure = pressure_doc.get("gpu") or {}
        level_doc = matrix.get(level_key) or {}
        guest = level_doc.get("guest") or {}
        overhead = level_doc.get("overhead") or {}

        active = runner_pressure.get("active_requests") or {}
        slots = runner_pressure.get("slot_count") or {}
        waiting = runner_pressure.get("waiting_requests") or {}
        deferred = runner_pressure.get("deferred_requests") or {}
        kv_usage = runner_pressure.get("kv_cache_usage") or {}
        gpu_util = gpu_pressure.get("gpu_util_pct") or {}
        gpu_power = gpu_pressure.get("power_draw_w") or {}
        models_guest = guest.get("models") or {}
        chat_guest = guest.get("chat") or {}
        stream_guest = guest.get("stream") or {}
        models = overhead.get("models") or {}
        chat = overhead.get("chat") or {}
        stream = overhead.get("stream") or {}
        workspace_count = report.get("workspace_count")
        effective_concurrency = (
            pressure_doc.get("effective_concurrency")
            or chat_guest.get("concurrency")
            or stream_guest.get("concurrency")
            or models_guest.get("concurrency")
        )

        row = {
            "run": label,
            **worker_meta,
            "workload": (
                f"ws={workspace_count} c={level} total={effective_concurrency}"
            ),
            "workspace_count": workspace_count,
            "per_workspace_concurrency": level,
            "effective_concurrency": effective_concurrency,
            "runner_slots": experiment.get("runner_slots"),
            "pressure_runner": classification.get("runner"),
            "pressure_gpu": classification.get("gpu"),
            "runner_active_median": stat(active, "median"),
            "runner_slot_median": stat(slots, "median"),
            "runner_waiting_max": stat(waiting, "max"),
            "runner_deferred_max": stat(deferred, "max"),
            "runner_kv_cache_usage_median": stat(kv_usage, "median"),
            "gpu_util_median": stat(gpu_util, "median"),
            "gpu_util_p95": stat(gpu_util, "p95"),
            "gpu_power_median_w": stat(gpu_power, "median"),
            "models_delta_ms": models.get("delta_ms"),
            "chat_delta_ms": chat.get("delta_ms"),
            "chat_p95_delta_ms": chat.get("p95_delta_ms"),
            "chat_ttfb_delta_ms": chat.get("ttfb_delta_ms"),
            "stream_delta_ms": stream.get("delta_ms"),
            "stream_p95_delta_ms": stream.get("p95_delta_ms"),
            "stream_ttfb_delta_ms": stream.get("ttfb_delta_ms"),
            "stream_chunk_gap_delta_ms": stream.get(
                "body_read_per_chunk_gap_delta_ms"
            ),
            "report": str(report_path),
            **broker,
        }
        rows.append(row)
    return rows


def add_baseline_deltas(rows: list[dict[str, Any]], baseline_run: str | None) -> None:
    baselines: dict[str, dict[str, Any]] = {}
    ordered_runs = []
    for row in rows:
        run = str(row.get("run") or "")
        if run not in ordered_runs:
            ordered_runs.append(run)
    baseline = baseline_run or (ordered_runs[0] if ordered_runs else None)
    if not baseline:
        return

    for row in rows:
        if row.get("run") == baseline:
            baselines[str(row.get("workload"))] = row

    for row in rows:
        row["baseline_run"] = baseline
        baseline_row = baselines.get(str(row.get("workload")))
        if not baseline_row:
            continue
        for field, out_field in BASELINE_FIELDS:
            row[out_field] = diff(row.get(field), baseline_row.get(field))


def sort_key(row: dict[str, Any]) -> tuple[Any, ...]:
    return (
        row.get("workspace_count") or 0,
        row.get("per_workspace_concurrency") or 0,
        row.get("effective_concurrency") or 0,
        row.get("run") or "",
    )


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
        "--run",
        action="append",
        required=True,
        metavar="LABEL=PROBE_JSON",
        help="labeled probe report to include; repeat for each runner",
    )
    parser.add_argument(
        "--broker",
        action="append",
        default=[],
        metavar="LABEL=COMPARISON_TSV",
        help="optional broker diagnostic comparison TSV for a labeled run",
    )
    parser.add_argument(
        "--baseline-run",
        help="run label to use for *_vs_baseline columns; defaults to first --run",
    )
    parser.add_argument(
        "--json",
        action="store_true",
        help="write a JSON array instead of tab-separated rows",
    )
    args = parser.parse_args()

    runs = [parse_assignment(raw, "--run") for raw in args.run]
    brokers = dict(parse_assignment(raw, "--broker") for raw in args.broker)

    labels = {label for label, _ in runs}
    unknown_brokers = sorted(set(brokers) - labels)
    if unknown_brokers:
        parser.error(
            "--broker label has no matching --run: " + ", ".join(unknown_brokers)
        )
    if args.baseline_run and args.baseline_run not in labels:
        parser.error("--baseline-run must match a --run label")

    rows: list[dict[str, Any]] = []
    for label, report_path in runs:
        rows.extend(rows_for_report(label, report_path, brokers.get(label)))
    rows = sorted(rows, key=sort_key)
    add_baseline_deltas(rows, args.baseline_run)

    if args.json:
        json.dump(rows, sys.stdout, indent=2, sort_keys=True)
        sys.stdout.write("\n")
    else:
        write_tsv(rows)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
