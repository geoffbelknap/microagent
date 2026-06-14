#!/usr/bin/env python3
"""A tiny host-side broker for the mediation channel.

This is the security boundary. It holds the credentials and data the agent
never sees, applies a policy to every request, logs each decision, and answers
over newline-delimited JSON. The agent can ask for anything; only the broker
can act, and it decides what's allowed.

`microagent` proxies the guest's vsock port to this host TCP address, and probes
it with a connect-and-close to compute readiness — so we tolerate empty
connections.
"""

from __future__ import annotations

import json
import socket
import sys
from datetime import datetime, timezone

HOST, PORT = "127.0.0.1", 9900

# Credentials and data live here on the host — never in the guest.
INBOX = [
    {"from": "alice@example.com", "subject": "Lunch Thursday?"},
    {"from": "noreply@bank.example", "subject": "Your statement is ready"},
]


def audit(decision: str, tool: str, detail: str = "") -> None:
    ts = datetime.now(timezone.utc).isoformat()
    print(f"[audit] {ts} {decision:5} {tool} {detail}".rstrip(), file=sys.stderr, flush=True)


def handle(req: dict) -> dict:
    tool = req.get("tool")
    args = req.get("args", {})

    if tool == "list_inbox":
        audit("ALLOW", tool)
        return {"ok": True, "inbox": INBOX}

    if tool == "send_email":
        to = args.get("to", "")
        # Policy: never send outside the org without explicit human approval.
        if not to.endswith("@example.com"):
            audit("DENY", tool, f"to={to} (external — needs human approval)")
            return {"ok": False, "error": "send to external address requires human approval"}
        audit("ALLOW", tool, f"to={to}")
        return {"ok": True, "queued": True}

    audit("DENY", tool or "<none>", "unknown tool")
    return {"ok": False, "error": f"unknown tool: {tool}"}


def serve(conn: socket.socket) -> None:
    rx = conn.makefile("r")
    line = rx.readline()
    if not line:
        return  # readiness probe: connect-and-close
    while line:
        msg = json.loads(line)
        if "signal" in msg:  # lifecycle signals share the channel
            print(f"broker: agent {msg['signal']}", flush=True)
        else:
            conn.sendall((json.dumps(handle(msg)) + "\n").encode())
        line = rx.readline()


def main() -> int:
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind((HOST, PORT))
    srv.listen(1)
    print(f"broker: listening on {HOST}:{PORT}", flush=True)
    while True:
        conn, _ = srv.accept()
        try:
            serve(conn)
        finally:
            conn.close()


if __name__ == "__main__":
    sys.exit(main())
