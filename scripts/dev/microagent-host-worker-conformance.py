#!/usr/bin/env python3
"""Check a runner-neutral OpenAI-compatible host-worker contract."""

from __future__ import annotations

import argparse
import json
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any


MAX_BODY = 64 * 1024 * 1024
MAX_TELEMETRY_BODY = 1024 * 1024
METRIC_RE = re.compile(
    r"^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{[^}]*\})?\s+"
    r"(-?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?)$"
)


class CheckError(RuntimeError):
    pass


def now_ms(start: float) -> float:
    return round((time.perf_counter() - start) * 1000, 3)


def normalize_base_url(raw_url: str) -> dict[str, Any]:
    raw_url = raw_url.strip()
    if not raw_url:
        raise CheckError("host worker URL must not be empty")
    if "://" not in raw_url:
        raw_url = "http://" + raw_url
    raw_url = raw_url.rstrip("/")
    parsed = urllib.parse.urlparse(raw_url)
    if parsed.scheme != "http":
        raise CheckError("host worker URL must use http:// for the bridge path")
    if parsed.username or parsed.password:
        raise CheckError("host worker URL must not include credentials")
    if parsed.query or parsed.fragment or parsed.params:
        raise CheckError(
            "host worker URL must not include query, fragment, or path params"
        )
    if not parsed.hostname:
        raise CheckError("host worker URL must include a host")
    try:
        port = parsed.port or 80
    except ValueError as err:
        raise CheckError(f"invalid host worker URL port: {err}") from err

    path = parsed.path.rstrip("/") or "/v1"
    netloc = parsed.netloc
    base_url = urllib.parse.urlunparse((parsed.scheme, netloc, path, "", "", ""))
    if path.endswith("/v1"):
        root_path = path[:-3].rstrip("/")
    else:
        root_path = ""
    root_url = urllib.parse.urlunparse(
        (parsed.scheme, netloc, root_path, "", "", "")
    ).rstrip("/")
    if not root_url:
        root_url = f"{parsed.scheme}://{netloc}"
    return {
        "base_path": path,
        "base_url": base_url,
        "host": parsed.hostname,
        "port": port,
        "root_url": root_url,
        "scheme": parsed.scheme,
    }


def url_join(base_url: str, path: str) -> str:
    return base_url.rstrip("/") + "/" + path.lstrip("/")


def request(
    method: str,
    url: str,
    *,
    body: bytes | None = None,
    headers: dict[str, str] | None = None,
    timeout: float,
    max_body: int,
) -> dict[str, Any]:
    req = urllib.request.Request(url, data=body, headers=headers or {}, method=method)
    start_epoch = time.time()
    start = time.perf_counter()
    out: dict[str, Any] = {
        "method": method,
        "ok": False,
        "start_epoch": round(start_epoch, 6),
        "url": url,
    }
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            first = resp.read(1)
            ttfb_ms = now_ms(start)
            rest = resp.read(max_body)
            body_bytes = first + rest
            out.update(
                {
                    "body_bytes": len(body_bytes),
                    "body_truncated": len(rest) >= max_body,
                    "content_type": resp.headers.get("Content-Type", ""),
                    "elapsed_ms": now_ms(start),
                    "headers": header_subset(resp.headers),
                    "ok": 200 <= resp.status < 300,
                    "status": resp.status,
                    "ttfb_ms": ttfb_ms,
                }
            )
            out["_body"] = body_bytes
            return out
    except urllib.error.HTTPError as err:
        body_bytes = err.read(max_body)
        out.update(
            {
                "body_bytes": len(body_bytes),
                "body_truncated": len(body_bytes) >= max_body,
                "content_type": err.headers.get("Content-Type", "")
                if err.headers
                else "",
                "elapsed_ms": now_ms(start),
                "error": str(err),
                "headers": header_subset(err.headers) if err.headers else {},
                "status": err.code,
                "ttfb_ms": None,
            }
        )
        out["_body"] = body_bytes
        return out
    except (OSError, TimeoutError, urllib.error.URLError) as err:
        out.update({"elapsed_ms": now_ms(start), "error": str(err)})
        out["_body"] = b""
        return out


def header_subset(headers: Any) -> dict[str, str]:
    keep = {
        "content-type",
        "server",
        "x-request-id",
        "x-microagent-mediation-request-id",
    }
    return {
        key: value
        for key, value in dict(headers).items()
        if key.lower() in keep
    }


def public_response(item: dict[str, Any]) -> dict[str, Any]:
    return {key: value for key, value in item.items() if not key.startswith("_")}


def parse_json_body(item: dict[str, Any], endpoint: str) -> Any:
    body = item.get("_body") or b""
    if not item.get("ok"):
        raise CheckError(
            f"{endpoint} returned status {item.get('status')}: {item.get('error')}"
        )
    try:
        return json.loads(body)
    except json.JSONDecodeError as err:
        raise CheckError(f"{endpoint} did not return JSON: {err}") from err


def model_ids_from(doc: Any) -> list[str]:
    if not isinstance(doc, dict):
        return []
    data = doc.get("data")
    if not isinstance(data, list):
        return []
    ids: list[str] = []
    for item in data:
        if isinstance(item, dict) and item.get("id"):
            ids.append(str(item["id"]))
    return ids


def check_models(base_url: str, timeout: float) -> tuple[dict[str, Any], list[str]]:
    item = request(
        "GET",
        url_join(base_url, "models"),
        timeout=timeout,
        max_body=4 * 1024 * 1024,
    )
    result = public_response(item)
    try:
        doc = parse_json_body(item, "/v1/models")
        ids = model_ids_from(doc)
        if not isinstance(doc, dict) or not isinstance(doc.get("data"), list):
            raise CheckError("/v1/models response must contain a data array")
        result.update(
            {
                "ok": True,
                "model_count": len(ids),
                "model_ids": ids[:20],
                "object": doc.get("object"),
            }
        )
        return result, ids
    except CheckError as err:
        result.update({"ok": False, "error": str(err)})
        return result, []


def chat_payload(model: str | None, *, stream: bool, max_tokens: int) -> bytes:
    doc: dict[str, Any] = {
        "messages": [{"role": "user", "content": "Reply with exactly: PONG"}],
        "max_tokens": max_tokens,
        "temperature": 0,
    }
    if model:
        doc["model"] = model
    if stream:
        doc["stream"] = True
    return json.dumps(doc, separators=(",", ":")).encode("utf-8")


def check_chat(
    base_url: str,
    model: str | None,
    timeout: float,
    max_tokens: int,
) -> dict[str, Any]:
    payload = chat_payload(model, stream=False, max_tokens=max_tokens)
    item = request(
        "POST",
        url_join(base_url, "chat/completions"),
        body=payload,
        headers={"Content-Type": "application/json"},
        timeout=timeout,
        max_body=4 * 1024 * 1024,
    )
    result = public_response(item)
    result["request_bytes"] = len(payload)
    try:
        doc = parse_json_body(item, "/v1/chat/completions")
        choices = doc.get("choices") if isinstance(doc, dict) else None
        if not isinstance(choices, list) or not choices:
            raise CheckError("/v1/chat/completions response must contain choices")
        result.update(
            {
                "choices": len(choices),
                "created": doc.get("created"),
                "id_present": bool(doc.get("id")),
                "model": doc.get("model"),
                "ok": True,
                "object": doc.get("object"),
            }
        )
    except CheckError as err:
        result.update({"ok": False, "error": str(err)})
    return result


def check_stream(
    base_url: str,
    model: str | None,
    timeout: float,
    max_tokens: int,
) -> dict[str, Any]:
    payload = chat_payload(model, stream=True, max_tokens=max_tokens)
    item = request(
        "POST",
        url_join(base_url, "chat/completions"),
        body=payload,
        headers={"Content-Type": "application/json"},
        timeout=timeout,
        max_body=MAX_BODY,
    )
    result = public_response(item)
    result["request_bytes"] = len(payload)
    text = (item.get("_body") or b"").decode("utf-8", errors="replace")
    data_lines = [line for line in text.splitlines() if line.startswith("data:")]
    result["chunks"] = len(data_lines)
    result["done_marker"] = "[DONE]" in text
    if not item.get("ok"):
        result.update(
            {
                "ok": False,
                "error": (
                    f"/v1/chat/completions stream returned status "
                    f"{item.get('status')}: {item.get('error')}"
                ),
            }
        )
        return result
    if not data_lines and '"choices"' not in text:
        result.update(
            {
                "ok": False,
                "error": "stream response must contain SSE data lines or choices",
            }
        )
        return result
    result["ok"] = True
    return result


def telemetry_format(content_type: str, body: bytes) -> tuple[str, dict[str, Any]]:
    text = body.decode("utf-8", errors="replace")
    stripped = text.lstrip()
    if "json" in content_type.lower() or stripped.startswith(("{", "[")):
        try:
            doc = json.loads(text)
        except json.JSONDecodeError:
            return "text", {}
        if isinstance(doc, dict):
            numeric_keys = [
                key
                for key, value in doc.items()
                if isinstance(value, (int, float)) and not isinstance(value, bool)
            ]
            return "json", {
                "json_object_keys": len(doc),
                "numeric_keys": numeric_keys[:40],
            }
        if isinstance(doc, list):
            return "json", {"json_items": len(doc)}
        return "json", {}

    metric_names: list[str] = []
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        match = METRIC_RE.match(line)
        if match:
            metric_names.append(match.group(1))
    if metric_names:
        unique = sorted(set(metric_names))
        return "prometheus", {
            "metric_count": len(metric_names),
            "metric_names": unique[:80],
        }
    return "text", {"line_count": len(text.splitlines())}


def check_optional_endpoints(
    root_url: str,
    endpoints: list[str],
    timeout: float,
) -> list[dict[str, Any]]:
    results = []
    for endpoint in endpoints:
        path = endpoint if endpoint.startswith("/") else "/" + endpoint
        item = request(
            "GET",
            url_join(root_url, path),
            timeout=timeout,
            max_body=MAX_TELEMETRY_BODY,
        )
        result = public_response(item)
        result["endpoint"] = path
        if item.get("_body"):
            sample_format, signals = telemetry_format(
                str(result.get("content_type") or ""),
                item["_body"],
            )
            result["format"] = sample_format
            result["signals"] = signals
        results.append(result)
    return results


def infer_runner_engine(
    explicit: str | None,
    optional_results: list[dict[str, Any]],
) -> str | None:
    if explicit:
        return explicit
    text = json.dumps(optional_results, sort_keys=True).lower()
    if "vllm" in text:
        return "vllm"
    if "llamacpp" in text or "llama" in text:
        return "llama.cpp"
    if "sglang" in text or "sgl_" in text:
        return "sglang"
    if "tensorrt" in text or "trtllm" in text:
        return "tensorrt-llm"
    return None


def source_for_optional(item: dict[str, Any]) -> str | None:
    if not item.get("ok"):
        return None
    endpoint = str(item.get("endpoint") or "unknown").lstrip("/") or "root"
    sample_format = str(item.get("format") or "unknown")
    return f"runner:{endpoint}:{sample_format}"


def build_report(args: argparse.Namespace) -> dict[str, Any]:
    info = normalize_base_url(args.base_url)
    base_url = info["base_url"]
    root_url = args.root_url.rstrip("/") if args.root_url else info["root_url"]
    optional_endpoints = [
        part.strip()
        for part in args.telemetry_endpoints.replace(" ", ",").split(",")
        if part.strip()
    ]

    models, model_ids = check_models(base_url, args.timeout)
    request_model = args.request_model or (model_ids[0] if model_ids else None)
    chat = check_chat(base_url, request_model, args.timeout, args.chat_tokens)
    stream = check_stream(base_url, request_model, args.timeout, args.stream_tokens)
    optional = check_optional_endpoints(
        root_url,
        optional_endpoints,
        args.telemetry_timeout,
    )
    required = {
        "models": models,
        "chat_completions": chat,
        "chat_completions_stream": stream,
    }
    required_ok = all(item.get("ok") is True for item in required.values())
    telemetry_sources = [
        source
        for item in optional
        if (source := source_for_optional(item)) is not None
    ]
    runner_engine = infer_runner_engine(args.runner_engine, optional)
    capabilities = {
        "chat_completions": chat.get("ok") is True,
        "metrics_available": any(
            item.get("endpoint") == "/metrics" and item.get("ok") is True
            for item in optional
        ),
        "model_count": models.get("model_count", 0),
        "models": models.get("ok") is True,
        "required_ok": required_ok,
        "runner_telemetry_available": bool(telemetry_sources),
        "runner_telemetry_sources": telemetry_sources,
        "slots_available": any(
            item.get("endpoint") == "/slots" and item.get("ok") is True
            for item in optional
        ),
        "streaming_chat_completions": stream.get("ok") is True,
    }
    report = {
        "schema_version": 1,
        "checked_at_epoch": round(time.time(), 6),
        "base_url": base_url,
        "root_url": root_url,
        "protocol": "openai-compatible",
        "runner_engine": runner_engine,
        "runner_version": args.runner_version or None,
        "request_model": request_model,
        "contract": {
            "required": [
                "GET /v1/models",
                "POST /v1/chat/completions",
                "POST /v1/chat/completions stream=true",
            ],
            "optional": optional_endpoints,
            "plain_http_required_for_microagent_bridge": True,
        },
        "capabilities": capabilities,
        "required": required,
        "optional": {"runner_telemetry": optional},
    }
    if args.label:
        report["label"] = args.label
    return report


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--root-url", default="")
    parser.add_argument("--request-model", default="")
    parser.add_argument("--runner-engine", default="")
    parser.add_argument("--runner-version", default="")
    parser.add_argument("--label", default="")
    parser.add_argument("--report", type=Path)
    parser.add_argument("--timeout", type=float, default=60.0)
    parser.add_argument("--telemetry-timeout", type=float, default=2.0)
    parser.add_argument("--chat-tokens", type=int, default=16)
    parser.add_argument("--stream-tokens", type=int, default=32)
    parser.add_argument(
        "--telemetry-endpoints",
        default="/metrics,/slots,/health,/version",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    report = build_report(args)
    rendered = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.report:
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(rendered, encoding="utf-8")
    sys.stdout.write(rendered)
    if not report["capabilities"]["required_ok"]:
        for name, item in report["required"].items():
            if not item.get("ok"):
                print(f"{name}: {item.get('error', 'failed')}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
