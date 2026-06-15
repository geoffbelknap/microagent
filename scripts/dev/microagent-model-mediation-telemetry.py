#!/usr/bin/env python3
"""Telemetry and budget helpers for production model-mediation E2Es."""

from __future__ import annotations

import argparse
import csv
import json
import math
import re
import shutil
import signal
import statistics
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any


METRIC_RE = re.compile(
    r"^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{[^}]*\})?\s+"
    r"(-?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?)$"
)
INTERESTING_METRIC_RE = re.compile(
    r"(llama|slot|queue|request|prompt|token|eval|cache|kv|batch|busy|process)",
    re.IGNORECASE,
)
ACTIVE_STATES = {"active", "busy", "processing", "generating", "decode", "prefill"}
BOOL_ACTIVE_KEYS = (
    "is_processing",
    "processing",
    "active",
    "busy",
    "is_busy",
    "is_generating",
    "has_task",
)
NUMERIC_SLOT_KEYS = (
    "n_ctx",
    "n_past",
    "n_prompt_tokens",
    "n_decoded",
    "n_remaining",
    "n_predict",
    "n_tokens",
    "n_cache_tokens",
    "prompt_tokens",
    "completion_tokens",
    "tokens",
)
GPU_QUERY_FIELDS = (
    "timestamp,index,utilization.gpu,utilization.memory,memory.used,"
    "memory.total,power.draw,pstate,clocks.current.sm,clocks.current.memory,"
    "temperature.gpu"
)
GPU_HEADER = (
    "host_epoch",
    "phase",
    "nvidia_timestamp",
    "gpu_index",
    "gpu_util_pct",
    "memory_util_pct",
    "memory_used_mib",
    "memory_total_mib",
    "power_draw_w",
    "pstate",
    "sm_clock_mhz",
    "memory_clock_mhz",
    "temperature_c",
)


def number(value: Any) -> float | None:
    if isinstance(value, bool) or value is None:
        return None
    if isinstance(value, (int, float)):
        return float(value)
    text = str(value).strip()
    if not text or text.upper() in {"N/A", "[NOT SUPPORTED]", "NOT SUPPORTED"}:
        return None
    try:
        return float(text)
    except ValueError:
        return None


def percentile(values: list[float], pct: float) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    idx = max(0, min(len(ordered) - 1, math.ceil((pct / 100) * len(ordered)) - 1))
    return ordered[idx]


def stats(values: list[float]) -> dict[str, float] | None:
    clean = sorted(value for value in values if value is not None)
    if not clean:
        return None
    return {
        "min": clean[0],
        "median": round(statistics.median(clean), 3),
        "mean": round(statistics.fmean(clean), 3),
        "p95": percentile(clean, 95) or clean[-1],
        "max": clean[-1],
    }


def fmt(value: Any) -> str:
    if value is None:
        return ""
    if isinstance(value, float):
        return f"{value:.3f}".rstrip("0").rstrip(".")
    return str(value)


def endpoint_url(root_url: str, endpoint: str) -> str:
    path = endpoint if endpoint.startswith("/") else "/" + endpoint
    return urllib.parse.urljoin(root_url.rstrip("/") + "/", path.lstrip("/"))


def item_active(item: dict[str, Any]) -> bool:
    for key in BOOL_ACTIVE_KEYS:
        if item.get(key) is True:
            return True
    state = str(item.get("state") or item.get("status") or "").strip().lower()
    return state in ACTIVE_STATES


def summarize_items(items: list[dict[str, Any]]) -> dict[str, float | int]:
    signals: dict[str, float | int] = {
        "json_items": len(items),
        "slot_count": len(items),
    }
    active_count = sum(1 for item in items if item_active(item))
    signals["active_slot_count"] = active_count
    signals["idle_slot_count"] = max(0, len(items) - active_count)
    for key in NUMERIC_SLOT_KEYS:
        values = [number(item.get(key)) for item in items]
        clean = [value for value in values if value is not None]
        if clean:
            signals[f"{key}_sum"] = round(sum(clean), 3)
            signals[f"{key}_max"] = round(max(clean), 3)
    return signals


def json_signals(doc: Any) -> dict[str, float | int]:
    if isinstance(doc, list):
        return summarize_items([item for item in doc if isinstance(item, dict)])
    if isinstance(doc, dict):
        for key in ("slots", "data", "items"):
            value = doc.get(key)
            if isinstance(value, list):
                signals = summarize_items([item for item in value if isinstance(item, dict)])
                signals["json_object_keys"] = len(doc)
                return signals
        signals: dict[str, float | int] = {"json_object_keys": len(doc)}
        for key, value in doc.items():
            value_num = number(value)
            if value_num is not None:
                signals[f"json_{key}"] = value_num
        return signals
    return {}


def prometheus_signals(text: str) -> dict[str, float | int]:
    totals: dict[str, float] = {}
    series_counts: dict[str, int] = {}
    for raw_line in text.splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        match = METRIC_RE.match(line)
        if not match:
            continue
        name = match.group(1)
        if name.endswith("_bucket") or not INTERESTING_METRIC_RE.search(name):
            continue
        try:
            value = float(match.group(2))
        except ValueError:
            continue
        totals[name] = totals.get(name, 0.0) + value
        series_counts[name] = series_counts.get(name, 0) + 1
    signals: dict[str, float | int] = {"metric_series": sum(series_counts.values())}
    for name in sorted(totals)[:120]:
        signals[f"metric_{name}_sum"] = round(totals[name], 6)
        signals[f"metric_{name}_series"] = series_counts[name]
    return signals


def summarize_body(content_type: str, body: bytes) -> tuple[str, dict[str, float | int]]:
    text = body.decode("utf-8", errors="replace")
    stripped = text.lstrip()
    if "json" in content_type.lower() or stripped.startswith(("[", "{")):
        try:
            return "json", json_signals(json.loads(text))
        except json.JSONDecodeError:
            pass
    return "prometheus", prometheus_signals(text)


def read_phase(path: Path) -> str:
    try:
        phase = path.read_text(encoding="utf-8").strip()
        return phase or "unknown"
    except OSError:
        return "unknown"


def sample_endpoint(root_url: str, endpoint: str, timeout: float, phase_file: Path) -> dict[str, Any]:
    url = endpoint_url(root_url, endpoint)
    start_epoch = time.time()
    start = time.perf_counter()
    sample: dict[str, Any] = {
        "host_epoch": round(start_epoch, 6),
        "phase": read_phase(phase_file),
        "endpoint": endpoint if endpoint.startswith("/") else "/" + endpoint,
        "url": url,
        "ok": False,
    }
    try:
        with urllib.request.urlopen(url, timeout=timeout) as response:
            body = response.read(1024 * 1024 + 1)
            content_type = response.headers.get("Content-Type", "")
            sample.update(
                {
                    "ok": 200 <= response.status < 300,
                    "status": response.status,
                    "elapsed_ms": round((time.perf_counter() - start) * 1000, 3),
                    "content_type": content_type,
                    "body_bytes": len(body),
                    "body_truncated": len(body) > 1024 * 1024,
                }
            )
            body_format, signals = summarize_body(content_type, body[: 1024 * 1024])
            sample["format"] = body_format
            sample["signals"] = signals
    except urllib.error.HTTPError as err:
        body = err.read(1024 * 1024 + 1)
        content_type = err.headers.get("Content-Type", "") if err.headers else ""
        sample.update(
            {
                "elapsed_ms": round((time.perf_counter() - start) * 1000, 3),
                "status": err.code,
                "content_type": content_type,
                "body_bytes": len(body),
                "body_truncated": len(body) > 1024 * 1024,
                "error": str(err),
            }
        )
        if body:
            body_format, signals = summarize_body(content_type, body[: 1024 * 1024])
            sample["format"] = body_format
            sample["signals"] = signals
    except (OSError, urllib.error.URLError, TimeoutError) as err:
        sample.update(
            {
                "elapsed_ms": round((time.perf_counter() - start) * 1000, 3),
                "error": str(err),
            }
        )
    return sample


def resolve_nvidia_smi(raw: str | None) -> str | None:
    if raw:
        return raw if Path(raw).exists() else None
    found = shutil.which("nvidia-smi")
    if found:
        return found
    wsl = Path("/usr/lib/wsl/lib/nvidia-smi")
    if wsl.exists():
        return str(wsl)
    return None


def gpu_available(nvidia_smi: str | None) -> bool:
    if not nvidia_smi:
        return False
    try:
        subprocess.run(
            [
                nvidia_smi,
                f"--query-gpu={GPU_QUERY_FIELDS}",
                "--format=csv,noheader,nounits",
            ],
            check=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            timeout=5,
        )
        return True
    except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired):
        return False


def sample_gpu(nvidia_smi: str, phase_file: Path) -> list[list[str]]:
    try:
        result = subprocess.run(
            [
                nvidia_smi,
                f"--query-gpu={GPU_QUERY_FIELDS}",
                "--format=csv,noheader,nounits",
            ],
            check=False,
            capture_output=True,
            text=True,
            timeout=5,
        )
    except (OSError, subprocess.TimeoutExpired):
        return []
    if result.returncode != 0:
        return []
    phase = read_phase(phase_file)
    host_epoch = f"{time.time():.6f}"
    rows: list[list[str]] = []
    for line in result.stdout.splitlines():
        if not line.strip():
            continue
        values = [part.strip() for part in line.split(",")]
        rows.append([host_epoch, phase, *values])
    return rows


def run_sample(args: argparse.Namespace) -> int:
    phase_file = Path(args.phase_file)
    phase_file.parent.mkdir(parents=True, exist_ok=True)
    if not phase_file.exists():
        phase_file.write_text("startup\n", encoding="utf-8")
    runner_out = Path(args.runner_out) if args.runner_out else None
    gpu_out = Path(args.gpu_out) if args.gpu_out else None
    endpoints = [part.strip() for part in args.endpoints.replace(" ", ",").split(",") if part.strip()]
    nvidia_smi = resolve_nvidia_smi(args.nvidia_smi)
    gpu_enabled = args.gpu != "off" and gpu_available(nvidia_smi)
    if args.gpu == "required" and not gpu_enabled:
        raise SystemExit("GPU telemetry required but nvidia-smi query is unavailable")

    stop = False

    def handle_stop(_signum: int, _frame: Any) -> None:
        nonlocal stop
        stop = True

    signal.signal(signal.SIGTERM, handle_stop)
    signal.signal(signal.SIGINT, handle_stop)

    runner_handle = None
    gpu_handle = None
    if runner_out:
        runner_out.parent.mkdir(parents=True, exist_ok=True)
        runner_handle = runner_out.open("a", encoding="utf-8")
    if gpu_enabled and gpu_out:
        gpu_out.parent.mkdir(parents=True, exist_ok=True)
        new_file = not gpu_out.exists() or gpu_out.stat().st_size == 0
        gpu_handle = gpu_out.open("a", encoding="utf-8", newline="")
        gpu_writer = csv.writer(gpu_handle)
        if new_file:
            gpu_writer.writerow(GPU_HEADER)
            gpu_handle.flush()
    else:
        gpu_writer = None

    try:
        while not stop:
            loop_start = time.perf_counter()
            if runner_handle:
                for endpoint in endpoints:
                    sample = sample_endpoint(args.runner_root_url, endpoint, args.timeout, phase_file)
                    runner_handle.write(json.dumps(sample, sort_keys=True) + "\n")
                runner_handle.flush()
            if gpu_enabled and gpu_writer and nvidia_smi:
                gpu_writer.writerows(sample_gpu(nvidia_smi, phase_file))
                gpu_handle.flush()
            remaining = args.interval - (time.perf_counter() - loop_start)
            time.sleep(max(0.05, remaining))
    finally:
        if runner_handle:
            runner_handle.close()
        if gpu_handle:
            gpu_handle.close()
    return 0


def load_runner_rows(path: Path | None) -> list[dict[str, Any]]:
    if not path or not path.exists():
        return []
    rows = []
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        try:
            rows.append(json.loads(line))
        except json.JSONDecodeError:
            continue
    return rows


def load_gpu_rows(path: Path | None) -> list[dict[str, str]]:
    if not path or not path.exists():
        return []
    with path.open(encoding="utf-8", newline="") as handle:
        return list(csv.DictReader(handle))


def stat_for_signal(rows: list[dict[str, Any]], endpoint: str, keys: tuple[str, ...]) -> dict[str, float] | None:
    values = []
    for row in rows:
        if row.get("endpoint") != endpoint:
            continue
        signals = row.get("signals") or {}
        for key in keys:
            if key in signals:
                value = number(signals.get(key))
                if value is not None:
                    values.append(value)
                break
    return stats(values)


def classify_runner(
    active: dict[str, float] | None,
    slots: dict[str, float] | None,
    waiting: dict[str, float] | None,
    deferred: dict[str, float] | None,
) -> tuple[str, float | None]:
    if waiting and (waiting.get("max") or 0) > 0:
        return "waiting_observed", None
    if deferred and (deferred.get("max") or 0) > 0:
        return "deferred_observed", None
    if active and slots and active.get("median") is not None and slots.get("median"):
        fraction = round(float(active["median"]) / float(slots["median"]), 3)
        if fraction >= 0.95:
            return "slots_saturated", fraction
        if fraction >= 0.5:
            return "slots_busy", fraction
        return "slots_available", fraction
    if active:
        return "active_observed", None
    return "unavailable", None


def classify_gpu(util: dict[str, float] | None) -> str:
    if not util:
        return "unavailable"
    median = util.get("median")
    p95 = util.get("p95")
    if (median is not None and median >= 85) or (p95 is not None and p95 >= 95):
        return "high"
    if (median is not None and median >= 50) or (p95 is not None and p95 >= 75):
        return "moderate"
    return "low"


def pressure_summary(runner_state: str, gpu_state: str) -> str:
    if runner_state in {"waiting_observed", "deferred_observed"}:
        return "runner reported queued or deferred work"
    if runner_state in {"slots_saturated", "slots_busy"} and gpu_state == "high":
        return "runner and GPU both showed high pressure"
    if runner_state == "slots_saturated":
        return "runner slots were saturated before GPU telemetry looked saturated"
    if runner_state == "slots_busy":
        return "runner slots were busy without clear GPU saturation"
    if runner_state == "slots_available" and gpu_state in {"low", "moderate"}:
        return "no clear runner or GPU saturation in sampled telemetry"
    if runner_state == "active_observed" and gpu_state in {"low", "moderate"}:
        return "runner had active work but no queued work or clear GPU saturation"
    if runner_state == "active_observed" and gpu_state == "high":
        return "runner had active work while GPU telemetry showed high pressure"
    if runner_state == "unavailable" and gpu_state == "unavailable":
        return "pressure telemetry unavailable"
    return "pressure source is inconclusive from sampled telemetry"


def run_summary(args: argparse.Namespace) -> int:
    runner_rows = load_runner_rows(Path(args.runner_in) if args.runner_in else None)
    gpu_rows = load_gpu_rows(Path(args.gpu_in) if args.gpu_in else None)
    phases = sorted(
        {
            str(row.get("phase") or "unknown")
            for row in runner_rows
        }
        | {row.get("phase") or "unknown" for row in gpu_rows}
    )
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    fields = (
        "phase",
        "adapter",
        "runner_state",
        "gpu_state",
        "active_median",
        "active_max",
        "slot_median",
        "active_fraction_median",
        "waiting_max",
        "deferred_max",
        "kv_cache_usage_median",
        "gpu_util_median",
        "gpu_util_p95",
        "gpu_util_max",
        "gpu_power_median_w",
        "gpu_power_p95_w",
        "sample_count_runner",
        "sample_count_gpu",
        "summary",
    )
    with out.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields, delimiter="\t")
        writer.writeheader()
        for phase in phases:
            phase_runner = [row for row in runner_rows if (row.get("phase") or "unknown") == phase]
            phase_gpu = [row for row in gpu_rows if (row.get("phase") or "unknown") == phase]
            active = stat_for_signal(
                phase_runner,
                "/metrics",
                (
                    "metric_vllm:num_requests_running_sum",
                    "metric_vllm:num_requests_processing_sum",
                    "json_num_requests_running",
                ),
            ) or stat_for_signal(phase_runner, "/slots", ("active_slot_count",))
            slots = stat_for_signal(phase_runner, "/slots", ("slot_count", "json_items"))
            waiting = stat_for_signal(
                phase_runner,
                "/metrics",
                (
                    "metric_vllm:num_requests_waiting_sum",
                    "metric_vllm:num_requests_waiting_by_reason_sum",
                    "json_num_requests_waiting",
                ),
            )
            deferred = stat_for_signal(
                phase_runner,
                "/metrics",
                (
                    "metric_llamacpp:requests_deferred_sum",
                    "metric_vllm:num_requests_waiting_by_reason_sum",
                    "json_num_requests_deferred",
                    "json_num_skipped_waiting_reqs",
                ),
            )
            kv_usage = stat_for_signal(
                phase_runner,
                "/metrics",
                (
                    "metric_vllm:kv_cache_usage_perc_sum",
                    "json_kv_cache_usage",
                    "json_kv_cache_usage_perc",
                ),
            )
            gpu_util = stats([value for row in phase_gpu if (value := number(row.get("gpu_util_pct"))) is not None])
            gpu_power = stats([value for row in phase_gpu if (value := number(row.get("power_draw_w"))) is not None])
            runner_state, active_fraction = classify_runner(active, slots, waiting, deferred)
            gpu_state = classify_gpu(gpu_util)
            writer.writerow(
                {
                    "phase": phase,
                    "adapter": args.adapter,
                    "runner_state": runner_state,
                    "gpu_state": gpu_state,
                    "active_median": fmt((active or {}).get("median")),
                    "active_max": fmt((active or {}).get("max")),
                    "slot_median": fmt((slots or {}).get("median")),
                    "active_fraction_median": fmt(active_fraction),
                    "waiting_max": fmt((waiting or {}).get("max")),
                    "deferred_max": fmt((deferred or {}).get("max")),
                    "kv_cache_usage_median": fmt((kv_usage or {}).get("median")),
                    "gpu_util_median": fmt((gpu_util or {}).get("median")),
                    "gpu_util_p95": fmt((gpu_util or {}).get("p95")),
                    "gpu_util_max": fmt((gpu_util or {}).get("max")),
                    "gpu_power_median_w": fmt((gpu_power or {}).get("median")),
                    "gpu_power_p95_w": fmt((gpu_power or {}).get("p95")),
                    "sample_count_runner": len(phase_runner),
                    "sample_count_gpu": len(phase_gpu),
                    "summary": pressure_summary(runner_state, gpu_state),
                }
            )
    return 0


def read_tsv(path: Path) -> list[dict[str, str]]:
    with path.open(encoding="utf-8", newline="") as handle:
        return list(csv.DictReader(handle, delimiter="\t"))


def gate_status(actual: float | None, limit: float, lower_is_better: bool = True) -> str:
    if actual is None:
        return "missing"
    if lower_is_better:
        return "pass" if actual <= limit else "fail"
    return "pass" if actual >= limit else "fail"


def run_gate(args: argparse.Namespace) -> int:
    comparisons = read_tsv(Path(args.profile_comparison))
    audits = read_tsv(Path(args.audit_summary))
    rows: list[dict[str, str]] = []

    def add_gate(name: str, actual: float | None, limit: float, detail: str) -> None:
        rows.append(
            {
                "gate": name,
                "actual": fmt(actual),
                "limit": fmt(limit),
                "status": gate_status(actual, limit),
                "detail": detail,
            }
        )

    for case in ("local", "pa"):
        for endpoint, column, limit, label in (
            ("models", "delta_total_p95_ms", args.max_models_total_p95_delta_ms, "models_total_p95_delta"),
            ("chat", "delta_total_p95_ms", args.max_chat_total_p95_delta_ms, "chat_total_p95_delta"),
            ("stream", "delta_ttfb_p95_ms", args.max_stream_ttfb_p95_delta_ms, "stream_ttfb_p95_delta"),
        ):
            match = next(
                (
                    row
                    for row in comparisons
                    if row.get("case") == case and row.get("endpoint") == endpoint
                ),
                None,
            )
            actual = number(match.get(column)) if match else None
            if actual is not None and actual < 0:
                actual = 0.0
            add_gate(f"{case}_{label}", actual, limit, f"{endpoint} {case} {column}")

    for row in audits:
        case = row.get("case") or ""
        if case in {"local", "pa", "pd", "pu"}:
            actual = number(row.get("decision_p95_ms"))
            add_gate(f"{case}_decision_p95", actual, args.max_decision_p95_ms, "audit decision_p95_ms")

    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    with out.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=("gate", "actual", "limit", "status", "detail"), delimiter="\t")
        writer.writeheader()
        writer.writerows(rows)

    failures = [row for row in rows if row["status"] != "pass"]
    if failures and args.mode == "required":
        for row in failures:
            print(
                f"{row['gate']} {row['status']}: actual={row['actual']} limit={row['limit']} {row['detail']}",
                file=sys.stderr,
            )
        return 1
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    sample = sub.add_parser("sample", help="sample runner and GPU telemetry until stopped")
    sample.add_argument("--runner-root-url", required=True)
    sample.add_argument("--phase-file", required=True)
    sample.add_argument("--runner-out")
    sample.add_argument("--gpu-out")
    sample.add_argument("--endpoints", default="/metrics,/slots,/health")
    sample.add_argument("--interval", type=float, default=0.5)
    sample.add_argument("--timeout", type=float, default=2.0)
    sample.add_argument("--gpu", choices=("off", "auto", "required"), default="auto")
    sample.add_argument("--nvidia-smi")
    sample.set_defaults(func=run_sample)

    summary = sub.add_parser("summary", help="summarize sampled telemetry")
    summary.add_argument("--runner-in")
    summary.add_argument("--gpu-in")
    summary.add_argument("--adapter", required=True)
    summary.add_argument("--out", required=True)
    summary.set_defaults(func=run_summary)

    gate = sub.add_parser("gate", help="evaluate mediation latency gates")
    gate.add_argument("--profile-comparison", required=True)
    gate.add_argument("--audit-summary", required=True)
    gate.add_argument("--out", required=True)
    gate.add_argument("--mode", choices=("off", "warn", "required"), default="required")
    gate.add_argument("--max-models-total-p95-delta-ms", type=float, default=100.0)
    gate.add_argument("--max-chat-total-p95-delta-ms", type=float, default=500.0)
    gate.add_argument("--max-stream-ttfb-p95-delta-ms", type=float, default=250.0)
    gate.add_argument("--max-decision-p95-ms", type=float, default=100.0)
    gate.set_defaults(func=run_gate)

    return parser


def main() -> int:
    args = build_parser().parse_args()
    if getattr(args, "mode", None) == "off":
        out = getattr(args, "out", None)
        if out:
            Path(out).write_text("gate\tactual\tlimit\tstatus\tdetail\n", encoding="utf-8")
        return 0
    return int(args.func(args))


if __name__ == "__main__":
    raise SystemExit(main())
