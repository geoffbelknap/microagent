#!/usr/bin/env python3
"""Measurement-only OpenAI-compatible host-worker broker."""

from __future__ import annotations

import argparse
import hashlib
import http.client
import http.server
import json
import socket
import socketserver
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from pathlib import Path
from typing import Any


HOP_BY_HOP_HEADERS = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
}


class NoDelayHTTPConnection(http.client.HTTPConnection):
    def connect(self) -> None:
        super().connect()
        # Tool-call shaped requests are often tiny; avoid avoidable delayed ACK noise.
        self.sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)


class ThreadingHTTPServer(socketserver.ThreadingMixIn, http.server.HTTPServer):
    allow_reuse_address = True
    daemon_threads = True


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target-base-url", required=True)
    parser.add_argument("--bind-host", default="127.0.0.1")
    parser.add_argument("--bind-port", type=int, default=0)
    parser.add_argument("--log-path", required=True, type=Path)
    parser.add_argument("--timeout", type=float, default=180.0)
    parser.add_argument(
        "--mediation-mode",
        choices=("passthrough", "local-allow", "policy"),
        default="passthrough",
    )
    parser.add_argument("--policy-url")
    parser.add_argument("--policy-timeout", type=float, default=2.0)
    parser.add_argument("--workspace-id", default="unknown")
    parser.add_argument("--capability", default="model.openai")
    parser.add_argument("--worker-id")
    args = parser.parse_args()

    target = urllib.parse.urlparse(args.target_base_url.rstrip("/"))
    if target.scheme != "http":
        raise SystemExit("broker target must use http://")
    if not target.hostname:
        raise SystemExit("broker target must include a host")
    if target.username or target.password:
        raise SystemExit("broker target must not include credentials")
    if target.query or target.fragment or target.params:
        raise SystemExit("broker target must not include query, fragment, or path parameters")
    if args.policy_url:
        policy_target = urllib.parse.urlparse(args.policy_url.rstrip("/"))
        if policy_target.scheme != "http":
            raise SystemExit("policy URL must use http://")
        if not policy_target.hostname:
            raise SystemExit("policy URL must include a host")
        if policy_target.username or policy_target.password:
            raise SystemExit("policy URL must not include credentials")
        if policy_target.query or policy_target.fragment or policy_target.params:
            raise SystemExit("policy URL must not include query, fragment, or path parameters")
    if args.mediation_mode == "policy" and not args.policy_url:
        raise SystemExit("--policy-url is required when --mediation-mode=policy")
    target_base_path = target.path.rstrip("/") or "/v1"
    target_port = target.port or 80
    worker_id = args.worker_id or f"{target.hostname}:{target_port}{target_base_path}"
    args.log_path.parent.mkdir(parents=True, exist_ok=True)
    log_file = args.log_path.open("a", encoding="utf-8", buffering=1)
    log_lock = threading.Lock()

    def write_log(event: str, **fields: Any) -> None:
        row = {
            "event": event,
            "host_epoch": round(time.time(), 6),
            **fields,
        }
        with log_lock:
            log_file.write(json.dumps(row, separators=(",", ":"), sort_keys=True) + "\n")

    def upstream_path_for(request_path: str) -> str:
        request_path = request_path or "/"
        if request_path == target_base_path or request_path.startswith(target_base_path + "/"):
            return request_path
        return request_path

    def elapsed_ms(start: float, end: float | None = None) -> float:
        if end is None:
            end = time.perf_counter()
        return round((end - start) * 1000, 3)

    def body_digest(body: bytes | None) -> str:
        return hashlib.sha256(body or b"").hexdigest()

    def build_decision_envelope(
        *,
        request_id: str,
        parsed: urllib.parse.SplitResult,
        method: str,
        upstream_path: str,
        request_bytes: int,
        request_body_sha256: str,
    ) -> dict[str, Any]:
        return {
            "schema_version": 1,
            "request_id": request_id,
            "workspace": {
                "id": args.workspace_id,
            },
            "capability": args.capability,
            "worker": {
                "id": worker_id,
                "protocol": "openai-compatible",
                "target_base_path": target_base_path,
            },
            "request": {
                "method": method,
                "path": parsed.path,
                "query": parsed.query,
                "upstream_path": upstream_path,
                "bytes": request_bytes,
                "body_sha256": request_body_sha256,
            },
            "limits": {
                "upstream_timeout_seconds": args.timeout,
                "policy_timeout_seconds": args.policy_timeout,
            },
            "deadline_epoch": round(time.time() + args.timeout, 6),
        }

    def policy_decision(envelope: dict[str, Any]) -> dict[str, Any]:
        if not args.policy_url:
            return {
                "decision": "error",
                "reason": "policy_url_missing",
                "http_status": None,
            }
        payload = json.dumps(envelope, separators=(",", ":"), sort_keys=True).encode("utf-8")
        request = urllib.request.Request(
            args.policy_url,
            data=payload,
            headers={
                "Content-Type": "application/json",
                "X-Microagent-Mediation-Request-ID": envelope["request_id"],
            },
            method="POST",
        )
        try:
            with urllib.request.urlopen(request, timeout=args.policy_timeout) as response:
                raw = response.read(1024 * 1024)
                http_status = response.status
        except urllib.error.HTTPError as err:
            err.read(1024 * 1024)
            return {
                "decision": "error",
                "reason": f"policy_http_{err.code}",
                "http_status": err.code,
            }
        except (OSError, TimeoutError, urllib.error.URLError) as err:
            return {
                "decision": "error",
                "reason": "policy_unavailable",
                "http_status": None,
                "error": str(err),
            }
        try:
            doc = json.loads(raw.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError):
            return {
                "decision": "error",
                "reason": "policy_invalid_json",
                "http_status": http_status,
            }
        decision = str(doc.get("decision") or doc.get("result") or "").lower()
        if decision not in {"allow", "deny"}:
            return {
                "decision": "error",
                "reason": "policy_invalid_decision",
                "http_status": http_status,
            }
        return {
            "decision": decision,
            "reason": str(doc.get("reason") or f"policy_{decision}"),
            "http_status": http_status,
            "audit_event_id": doc.get("audit_event_id"),
        }

    def evaluate_mediation(
        *,
        request_id: str,
        parsed: urllib.parse.SplitResult,
        method: str,
        upstream_path: str,
        request_bytes: int,
        request_body_sha256: str,
        start: float,
    ) -> dict[str, Any]:
        if args.mediation_mode == "passthrough":
            write_log(
                "mediation_bypass",
                request_id=request_id,
                method=method,
                path=parsed.path,
                mediation_mode=args.mediation_mode,
                elapsed_ms=elapsed_ms(start),
            )
            return {
                "decision": "allow",
                "reason": "passthrough",
                "source": "passthrough",
                "decision_ms": None,
                "http_status": None,
                "audit_event_id": None,
            }

        envelope = build_decision_envelope(
            request_id=request_id,
            parsed=parsed,
            method=method,
            upstream_path=upstream_path,
            request_bytes=request_bytes,
            request_body_sha256=request_body_sha256,
        )
        decision_start = time.perf_counter()
        write_log(
            "mediation_decision_request",
            request_id=request_id,
            method=method,
            path=parsed.path,
            mediation_mode=args.mediation_mode,
            workspace_id=args.workspace_id,
            capability=args.capability,
            worker_id=worker_id,
            request_bytes=request_bytes,
            request_body_sha256=request_body_sha256,
            elapsed_ms=elapsed_ms(start, decision_start),
        )
        if args.mediation_mode == "local-allow":
            decision = {
                "decision": "allow",
                "reason": "local_allow",
                "source": "local",
                "http_status": None,
                "audit_event_id": f"local:{request_id}",
            }
        else:
            decision = policy_decision(envelope)
            decision["source"] = "policy"
        decision_ms = elapsed_ms(decision_start)
        decision["decision_ms"] = decision_ms
        decision_event = {
            "allow": "mediation_decision_allow",
            "deny": "mediation_decision_deny",
        }.get(str(decision.get("decision")), "mediation_decision_error")
        write_log(
            decision_event,
            request_id=request_id,
            method=method,
            path=parsed.path,
            mediation_mode=args.mediation_mode,
            mediation_result=decision.get("decision"),
            mediation_reason=decision.get("reason"),
            mediation_decision_ms=decision_ms,
            mediation_policy_status=decision.get("http_status"),
            audit_event_id=decision.get("audit_event_id"),
            elapsed_ms=elapsed_ms(start),
        )
        return decision

    class BrokerHandler(http.server.BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"
        server_version = "microagent-host-worker-broker/0"

        def setup(self) -> None:
            super().setup()
            # Match upstream behavior so the broker does not add small-packet latency.
            self.connection.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)

        def log_message(self, _format: str, *args: Any) -> None:
            return

        def do_GET(self) -> None:
            self.proxy()

        def do_POST(self) -> None:
            self.proxy()

        def do_HEAD(self) -> None:
            self.proxy()

        def do_OPTIONS(self) -> None:
            self.proxy()

        def proxy(self) -> None:
            request_id = str(uuid.uuid4())
            start = time.perf_counter()
            parsed = urllib.parse.urlsplit(self.path)
            if parsed.path == "/healthz":
                body = b"OK\n"
                self.send_response(200)
                self.send_header("Content-Type", "text/plain")
                self.send_header("Content-Length", str(len(body)))
                self.send_header("X-Microagent-Mediation-Request-ID", request_id)
                self.end_headers()
                if self.command != "HEAD":
                    self.wfile.write(body)
                return

            write_log(
                "request_accept",
                request_id=request_id,
                method=self.command,
                path=parsed.path,
                query=parsed.query,
            )
            content_length = self.headers.get("Content-Length")
            request_bytes = 0
            body = None
            if content_length:
                request_bytes = int(content_length)
                body = self.rfile.read(request_bytes)
            request_body_sha256 = body_digest(body)
            request_body_read_ms = elapsed_ms(start)
            write_log(
                "request_body_read",
                request_id=request_id,
                method=self.command,
                path=parsed.path,
                request_body_read_ms=request_body_read_ms,
                request_bytes=request_bytes,
                request_body_sha256=request_body_sha256,
            )

            upstream_path = upstream_path_for(parsed.path)
            if parsed.query:
                upstream_path = upstream_path + "?" + parsed.query
            headers = {
                key: value
                for key, value in self.headers.items()
                if key.lower() not in HOP_BY_HOP_HEADERS and key.lower() != "host"
            }
            headers["Host"] = target.netloc
            headers["X-Microagent-Mediation-Request-ID"] = request_id

            write_log(
                "request_start",
                request_id=request_id,
                method=self.command,
                path=parsed.path,
                query=parsed.query,
                request_bytes=request_bytes,
                upstream_host=target.hostname,
                upstream_path=upstream_path,
                upstream_port=target_port,
            )
            mediation = evaluate_mediation(
                request_id=request_id,
                parsed=parsed,
                method=self.command,
                upstream_path=upstream_path,
                request_bytes=request_bytes,
                request_body_sha256=request_body_sha256,
                start=start,
            )
            mediation_decision_ms = mediation.get("decision_ms")
            mediation_result = mediation.get("decision")
            mediation_reason = mediation.get("reason")
            mediation_policy_status = mediation.get("http_status")
            mediation_audit_event_id = mediation.get("audit_event_id")
            response_started = False
            response_bytes = 0
            status = None
            upstream_request_write_ms = None
            upstream_ttfb_ms = None
            upstream_first_body_byte_ms = None
            downstream_first_body_byte_ms = None
            response_body_ms = None
            downstream_complete_ms = None
            conn = None
            try:
                if mediation_result != "allow":
                    status = 403 if mediation_result == "deny" else 503
                    body_err = json.dumps(
                        {
                            "error": {
                                "message": "mediation denied",
                                "reason": mediation_reason,
                                "request_id": request_id,
                            }
                        },
                        separators=(",", ":"),
                        sort_keys=True,
                    ).encode("utf-8") + b"\n"
                    response_bytes = len(body_err)
                    self.send_response(status)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(response_bytes))
                    self.send_header("Connection", "close")
                    self.send_header("X-Microagent-Mediation-Request-ID", request_id)
                    self.end_headers()
                    response_started = True
                    if self.command != "HEAD":
                        self.wfile.write(body_err)
                        self.wfile.flush()
                    response_end = time.perf_counter()
                    downstream_complete_ms = elapsed_ms(start, response_end)
                    write_log(
                        "request_denied",
                        request_id=request_id,
                        method=self.command,
                        path=parsed.path,
                        status=status,
                        mediation_mode=args.mediation_mode,
                        mediation_result=mediation_result,
                        mediation_reason=mediation_reason,
                        mediation_decision_ms=mediation_decision_ms,
                        mediation_policy_status=mediation_policy_status,
                        audit_event_id=mediation_audit_event_id,
                        duration_ms=downstream_complete_ms,
                    )
                    write_log(
                        "request_end",
                        request_id=request_id,
                        method=self.command,
                        path=parsed.path,
                        request_bytes=request_bytes,
                        request_body_sha256=request_body_sha256,
                        response_bytes=response_bytes,
                        status=status,
                        duration_ms=downstream_complete_ms,
                        request_body_read_ms=request_body_read_ms,
                        mediation_mode=args.mediation_mode,
                        mediation_result=mediation_result,
                        mediation_reason=mediation_reason,
                        mediation_decision_ms=mediation_decision_ms,
                        mediation_policy_status=mediation_policy_status,
                        audit_event_id=mediation_audit_event_id,
                        upstream_request_write_ms=upstream_request_write_ms,
                        upstream_ttfb_ms=upstream_ttfb_ms,
                        upstream_first_body_byte_ms=upstream_first_body_byte_ms,
                        downstream_first_body_byte_ms=downstream_first_body_byte_ms,
                        response_body_ms=response_body_ms,
                        downstream_complete_ms=downstream_complete_ms,
                    )
                    self.close_connection = True
                    return

                conn = NoDelayHTTPConnection(target.hostname, target_port, timeout=args.timeout)
                upstream_start = time.perf_counter()
                write_log(
                    "upstream_request_start",
                    request_id=request_id,
                    method=self.command,
                    path=parsed.path,
                    upstream_path=upstream_path,
                    mediation_mode=args.mediation_mode,
                    mediation_result=mediation_result,
                    elapsed_ms=elapsed_ms(start, upstream_start),
                )
                conn.request(self.command, upstream_path, body=body, headers=headers)
                upstream_request_sent = time.perf_counter()
                upstream_request_write_ms = elapsed_ms(upstream_start, upstream_request_sent)
                write_log(
                    "upstream_request_sent",
                    request_id=request_id,
                    method=self.command,
                    path=parsed.path,
                    upstream_request_write_ms=upstream_request_write_ms,
                    mediation_mode=args.mediation_mode,
                    mediation_result=mediation_result,
                    elapsed_ms=elapsed_ms(start, upstream_request_sent),
                )
                response = conn.getresponse()
                upstream_headers = time.perf_counter()
                upstream_ttfb_ms = elapsed_ms(upstream_start, upstream_headers)
                status = response.status
                write_log(
                    "upstream_headers",
                    request_id=request_id,
                    method=self.command,
                    path=parsed.path,
                    status=status,
                    upstream_ttfb_ms=upstream_ttfb_ms,
                    mediation_mode=args.mediation_mode,
                    mediation_result=mediation_result,
                    elapsed_ms=elapsed_ms(start, upstream_headers),
                )
                self.send_response(response.status, response.reason)
                has_length = False
                for key, value in response.getheaders():
                    lower = key.lower()
                    if lower in HOP_BY_HOP_HEADERS:
                        continue
                    if lower == "content-length":
                        has_length = True
                    self.send_header(key, value)
                self.send_header("X-Microagent-Mediation-Request-ID", request_id)
                if not has_length:
                    self.send_header("Connection", "close")
                    self.close_connection = True
                self.end_headers()
                response_started = True
                downstream_headers_sent = time.perf_counter()
                write_log(
                    "downstream_headers_sent",
                    request_id=request_id,
                    method=self.command,
                    path=parsed.path,
                    status=status,
                    elapsed_ms=elapsed_ms(start, downstream_headers_sent),
                    downstream_header_write_ms=elapsed_ms(
                        upstream_headers, downstream_headers_sent
                    ),
                )
                if self.command != "HEAD":
                    reader = response.read1 if hasattr(response, "read1") else response.read
                    first_body_started = None
                    while True:
                        chunk = reader(4096)
                        if not chunk:
                            break
                        chunk_read_at = time.perf_counter()
                        if first_body_started is None:
                            first_body_started = chunk_read_at
                            upstream_first_body_byte_ms = elapsed_ms(
                                upstream_start, chunk_read_at
                            )
                            write_log(
                                "upstream_first_body_byte",
                                request_id=request_id,
                                method=self.command,
                                path=parsed.path,
                                upstream_first_body_byte_ms=upstream_first_body_byte_ms,
                                elapsed_ms=elapsed_ms(start, chunk_read_at),
                            )
                        response_bytes += len(chunk)
                        self.wfile.write(chunk)
                        self.wfile.flush()
                        chunk_sent_at = time.perf_counter()
                        if downstream_first_body_byte_ms is None:
                            downstream_first_body_byte_ms = elapsed_ms(
                                start, chunk_sent_at
                            )
                            write_log(
                                "downstream_first_body_byte",
                                request_id=request_id,
                                method=self.command,
                                path=parsed.path,
                                downstream_first_body_byte_ms=downstream_first_body_byte_ms,
                                elapsed_ms=downstream_first_body_byte_ms,
                            )
                    response_end = time.perf_counter()
                    if first_body_started is not None:
                        response_body_ms = elapsed_ms(first_body_started, response_end)
                else:
                    response_end = time.perf_counter()
                downstream_complete_ms = elapsed_ms(start, response_end)
                duration_ms = downstream_complete_ms
                write_log(
                    "request_end",
                    request_id=request_id,
                    method=self.command,
                    path=parsed.path,
                    request_bytes=request_bytes,
                    response_bytes=response_bytes,
                    status=status,
                    duration_ms=duration_ms,
                    request_body_read_ms=request_body_read_ms,
                    request_body_sha256=request_body_sha256,
                    mediation_mode=args.mediation_mode,
                    mediation_result=mediation_result,
                    mediation_reason=mediation_reason,
                    mediation_decision_ms=mediation_decision_ms,
                    mediation_policy_status=mediation_policy_status,
                    audit_event_id=mediation_audit_event_id,
                    upstream_request_write_ms=upstream_request_write_ms,
                    upstream_ttfb_ms=upstream_ttfb_ms,
                    upstream_first_body_byte_ms=upstream_first_body_byte_ms,
                    downstream_first_body_byte_ms=downstream_first_body_byte_ms,
                    response_body_ms=response_body_ms,
                    downstream_complete_ms=downstream_complete_ms,
                )
            except Exception as err:  # noqa: BLE001 - broker logs and returns a bounded 502.
                duration_ms = elapsed_ms(start)
                write_log(
                    "request_error",
                    request_id=request_id,
                    method=self.command,
                    path=parsed.path,
                    request_bytes=request_bytes,
                    response_bytes=response_bytes,
                    status=status,
                    duration_ms=duration_ms,
                    request_body_read_ms=request_body_read_ms,
                    request_body_sha256=request_body_sha256,
                    mediation_mode=args.mediation_mode,
                    mediation_result=mediation_result,
                    mediation_reason=mediation_reason,
                    mediation_decision_ms=mediation_decision_ms,
                    mediation_policy_status=mediation_policy_status,
                    audit_event_id=mediation_audit_event_id,
                    upstream_request_write_ms=upstream_request_write_ms,
                    upstream_ttfb_ms=upstream_ttfb_ms,
                    upstream_first_body_byte_ms=upstream_first_body_byte_ms,
                    downstream_first_body_byte_ms=downstream_first_body_byte_ms,
                    response_body_ms=response_body_ms,
                    downstream_complete_ms=downstream_complete_ms,
                    error=str(err),
                )
                if not response_started:
                    body_err = b"upstream error\n"
                    self.send_response(502)
                    self.send_header("Content-Type", "text/plain")
                    self.send_header("Content-Length", str(len(body_err)))
                    self.send_header("Connection", "close")
                    self.send_header("X-Microagent-Mediation-Request-ID", request_id)
                    self.end_headers()
                    if self.command != "HEAD":
                        self.wfile.write(body_err)
                self.close_connection = True
            finally:
                if conn is not None:
                    conn.close()

    server = ThreadingHTTPServer((args.bind_host, args.bind_port), BrokerHandler)
    write_log(
        "broker_start",
        listen_host=args.bind_host,
        listen_port=server.server_address[1],
        mediation_mode=args.mediation_mode,
        policy_url=args.policy_url,
        target_base_path=target_base_path,
        target_base_url=args.target_base_url,
        worker_id=worker_id,
        workspace_id=args.workspace_id,
    )
    print(f"ready {args.bind_host}:{server.server_address[1]}", flush=True)
    try:
        server.serve_forever()
    finally:
        write_log("broker_stop")
        log_file.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
