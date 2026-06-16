#!/usr/bin/env python3
"""Minimal policy endpoint for host-worker mediation experiments."""

from __future__ import annotations

import argparse
import http.server
import json
import socketserver
import threading
import time
import uuid
from pathlib import Path
from typing import Any


class ThreadingHTTPServer(socketserver.ThreadingMixIn, http.server.HTTPServer):
    allow_reuse_address = True
    daemon_threads = True


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bind-host", default="127.0.0.1")
    parser.add_argument("--bind-port", type=int, default=0)
    parser.add_argument("--decision", choices=("allow", "deny"), default="allow")
    parser.add_argument("--log-path", required=True, type=Path)
    parser.add_argument("--delay-ms", type=float, default=0.0)
    args = parser.parse_args()

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

    class PolicyHandler(http.server.BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"
        server_version = "microagent-host-worker-policy-stub/0"

        def log_message(self, _format: str, *args_inner: Any) -> None:
            return

        def do_GET(self) -> None:
            if self.path != "/healthz":
                self.send_error(404)
                return
            body = b"OK\n"
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(body)
            self.close_connection = True

        def do_POST(self) -> None:
            content_length = int(self.headers.get("Content-Length") or 0)
            body = self.rfile.read(content_length) if content_length else b""
            try:
                envelope = json.loads(body.decode("utf-8")) if body else {}
            except (UnicodeDecodeError, json.JSONDecodeError):
                envelope = {}
            request_id = str(envelope.get("request_id") or self.headers.get("X-Microagent-Mediation-Request-ID") or "")
            if args.delay_ms > 0:
                time.sleep(args.delay_ms / 1000.0)
            audit_event_id = f"stub:{args.decision}:{request_id or uuid.uuid4()}"
            write_log(
                "policy_decision",
                request_id=request_id,
                decision=args.decision,
                audit_event_id=audit_event_id,
                workspace_id=(envelope.get("workspace") or {}).get("id"),
                capability=envelope.get("capability"),
                request_path=(envelope.get("request") or {}).get("path"),
                request_bytes=(envelope.get("request") or {}).get("bytes"),
                request_body_sha256=(envelope.get("request") or {}).get("body_sha256"),
            )
            response = json.dumps(
                {
                    "decision": args.decision,
                    "reason": f"stub_{args.decision}",
                    "audit_event_id": audit_event_id,
                },
                separators=(",", ":"),
                sort_keys=True,
            ).encode("utf-8") + b"\n"
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(response)))
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(response)
            self.close_connection = True

    server = ThreadingHTTPServer((args.bind_host, args.bind_port), PolicyHandler)
    write_log(
        "policy_stub_start",
        decision=args.decision,
        listen_host=args.bind_host,
        listen_port=server.server_address[1],
    )
    print(f"ready {args.bind_host}:{server.server_address[1]}", flush=True)
    try:
        server.serve_forever()
    finally:
        write_log("policy_stub_stop")
        log_file.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
