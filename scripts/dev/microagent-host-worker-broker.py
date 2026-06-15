#!/usr/bin/env python3
"""Measurement-only OpenAI-compatible host-worker broker."""

from __future__ import annotations

import argparse
import http.client
import http.server
import json
import socket
import socketserver
import threading
import time
import urllib.parse
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
    args = parser.parse_args()

    target = urllib.parse.urlparse(args.target_base_url.rstrip("/"))
    if target.scheme != "http":
        raise SystemExit("broker target must use http://")
    if not target.hostname:
        raise SystemExit("broker target must include a host")
    target_base_path = target.path.rstrip("/") or "/v1"
    target_port = target.port or 80
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

            content_length = self.headers.get("Content-Length")
            request_bytes = 0
            body = None
            if content_length:
                request_bytes = int(content_length)
                body = self.rfile.read(request_bytes)

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

            start = time.perf_counter()
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
            conn = NoDelayHTTPConnection(target.hostname, target_port, timeout=args.timeout)
            response_started = False
            response_bytes = 0
            status = None
            try:
                conn.request(self.command, upstream_path, body=body, headers=headers)
                response = conn.getresponse()
                status = response.status
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
                if self.command != "HEAD":
                    reader = response.read1 if hasattr(response, "read1") else response.read
                    while True:
                        chunk = reader(4096)
                        if not chunk:
                            break
                        response_bytes += len(chunk)
                        self.wfile.write(chunk)
                        self.wfile.flush()
                duration_ms = round((time.perf_counter() - start) * 1000, 3)
                write_log(
                    "request_end",
                    request_id=request_id,
                    method=self.command,
                    path=parsed.path,
                    request_bytes=request_bytes,
                    response_bytes=response_bytes,
                    status=status,
                    duration_ms=duration_ms,
                )
            except Exception as err:  # noqa: BLE001 - broker logs and returns a bounded 502.
                duration_ms = round((time.perf_counter() - start) * 1000, 3)
                write_log(
                    "request_error",
                    request_id=request_id,
                    method=self.command,
                    path=parsed.path,
                    request_bytes=request_bytes,
                    response_bytes=response_bytes,
                    status=status,
                    duration_ms=duration_ms,
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
                conn.close()

    server = ThreadingHTTPServer((args.bind_host, args.bind_port), BrokerHandler)
    write_log(
        "broker_start",
        listen_host=args.bind_host,
        listen_port=server.server_address[1],
        target_base_path=target_base_path,
        target_base_url=args.target_base_url,
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
