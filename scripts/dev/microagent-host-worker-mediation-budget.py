#!/usr/bin/env python3
"""Summarize and optionally enforce experimental host-worker mediation budgets."""

from __future__ import annotations

import argparse
import csv
import json
import sys
from pathlib import Path
from typing import Any


def read_tsv(path: Path) -> list[dict[str, str]]:
    with path.open(encoding="utf-8", newline="") as handle:
        return list(csv.DictReader(handle, delimiter="\t"))


def as_float(value: Any) -> float | None:
    if value is None or value == "":
        return None
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def status_for(actual: float | None, budget: float) -> str:
    if actual is None:
        return "missing"
    if actual <= budget:
        return "pass"
    return "fail"


def add_budget_row(
    rows: list[dict[str, Any]],
    *,
    scope: str,
    metric: str,
    actual: float | None,
    budget: float,
    source: str,
) -> None:
    rows.append(
        {
            "scope": scope,
            "metric": metric,
            "actual": actual,
            "budget": budget,
            "status": status_for(actual, budget),
            "source": source,
        }
    )


def write_tsv(path: Path, rows: list[dict[str, Any]]) -> None:
    fields = ("scope", "metric", "actual", "budget", "status", "source")
    with path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields, delimiter="\t", lineterminator="\n")
        writer.writeheader()
        writer.writerows(rows)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--comparison", required=True, type=Path)
    parser.add_argument("--broker-summary", required=True, type=Path)
    parser.add_argument("--output-json", type=Path)
    parser.add_argument("--output-tsv", type=Path)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--models-p95-ms", type=float, default=15.0)
    parser.add_argument("--chat-p95-ms", type=float, default=50.0)
    parser.add_argument("--stream-ttfb-p95-ms", type=float, default=75.0)
    parser.add_argument("--broker-request-body-read-p95-ms", type=float, default=5.0)
    parser.add_argument("--broker-mediation-decision-p95-ms", type=float, default=25.0)
    parser.add_argument("--broker-upstream-request-write-p95-ms", type=float, default=5.0)
    parser.add_argument("--broker-error-count", type=float, default=0.0)
    args = parser.parse_args()

    comparison_rows = read_tsv(args.comparison)
    broker_rows = read_tsv(args.broker_summary)

    by_endpoint = {row.get("endpoint", ""): row for row in comparison_rows}
    budget_rows: list[dict[str, Any]] = []

    add_budget_row(
        budget_rows,
        scope="comparison:models",
        metric="host_broker_overhead_p95_ms",
        actual=as_float(by_endpoint.get("models", {}).get("host_broker_overhead_p95_ms")),
        budget=args.models_p95_ms,
        source=str(args.comparison),
    )
    add_budget_row(
        budget_rows,
        scope="comparison:chat",
        metric="host_broker_overhead_p95_ms",
        actual=as_float(by_endpoint.get("chat", {}).get("host_broker_overhead_p95_ms")),
        budget=args.chat_p95_ms,
        source=str(args.comparison),
    )
    add_budget_row(
        budget_rows,
        scope="comparison:stream",
        metric="host_broker_ttfb_overhead_p95_ms",
        actual=as_float(by_endpoint.get("stream", {}).get("host_broker_ttfb_overhead_p95_ms")),
        budget=args.stream_ttfb_p95_ms,
        source=str(args.comparison),
    )

    for row in broker_rows:
        path = row.get("path") or "<unknown>"
        add_budget_row(
            budget_rows,
            scope=f"broker:{path}",
            metric="error_count",
            actual=as_float(row.get("error_count")),
            budget=args.broker_error_count,
            source=str(args.broker_summary),
        )
        add_budget_row(
            budget_rows,
            scope=f"broker:{path}",
            metric="request_body_read_p95_ms",
            actual=as_float(row.get("request_body_read_p95_ms")),
            budget=args.broker_request_body_read_p95_ms,
            source=str(args.broker_summary),
        )
        decision_p95 = as_float(row.get("mediation_decision_p95_ms"))
        if decision_p95 is not None:
            add_budget_row(
                budget_rows,
                scope=f"broker:{path}",
                metric="mediation_decision_p95_ms",
                actual=decision_p95,
                budget=args.broker_mediation_decision_p95_ms,
                source=str(args.broker_summary),
            )
        add_budget_row(
            budget_rows,
            scope=f"broker:{path}",
            metric="upstream_request_write_p95_ms",
            actual=as_float(row.get("upstream_request_write_p95_ms")),
            budget=args.broker_upstream_request_write_p95_ms,
            source=str(args.broker_summary),
        )

    status_counts: dict[str, int] = {}
    for row in budget_rows:
        status_counts[row["status"]] = status_counts.get(row["status"], 0) + 1

    summary = {
        "schema_version": 1,
        "check": args.check,
        "status": "pass" if status_counts.get("fail", 0) == 0 and status_counts.get("missing", 0) == 0 else "fail",
        "status_counts": status_counts,
        "budgets": budget_rows,
        "notes": [
            "comparison gates use host_broker fields to isolate broker-added overhead",
            "guest_broker fields remain diagnostic because they include guest bridge scheduling noise",
            "stream total duration is intentionally not budgeted here",
            "use stream TTFB and broker phase timing for the first mediation gate",
        ],
    }

    if args.output_json:
        args.output_json.write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    if args.output_tsv:
        write_tsv(args.output_tsv, budget_rows)
    if not args.output_json and not args.output_tsv:
        print(json.dumps(summary, indent=2, sort_keys=True))

    if args.check and summary["status"] != "pass":
        failed = [
            row
            for row in budget_rows
            if row["status"] in {"fail", "missing"}
        ]
        for row in failed:
            print(
                f"budget {row['status']}: {row['scope']} {row['metric']} "
                f"actual={row['actual']} budget={row['budget']}",
                file=sys.stderr,
            )
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
