#!/usr/bin/env python3
"""Use a credential the guest can never read from disk.

The secret is declared `--secret-on-demand`, so it is never written to
/run/secrets and never captured in a snapshot. The agent asks for it over the
secrets socket only at the instant it needs it, uses it, and never persists the
value. With `--secrets-audit`, every fetch is recorded host-side.

This proves two things at once: the value isn't sitting on disk (`on_disk` is
False), and a name that wasn't declared on-demand is refused.
"""

from __future__ import annotations

import base64
import json
import os
import socket
import sys
from pathlib import Path


def fetch(name: str) -> bytes:
    """Ask the host for a secret over the on-demand secrets socket."""
    sock = socket.socket(socket.AF_UNIX)
    sock.connect(os.environ["MICROAGENT_SECRETS_SOCK"])
    sock.sendall(f"GET {name}\n".encode())
    resp = json.loads(sock.recv(8192).decode())
    if not resp.get("ok"):
        raise PermissionError(f"{name}: {resp.get('error', 'denied')}")
    return base64.b64decode(resp["value"])


def main() -> int:
    token = fetch("API_TOKEN")  # used, never printed or persisted

    # A name not declared on-demand must be refused.
    try:
        fetch("ROOT_PASSWORD")
        undeclared_denied = False
    except PermissionError:
        undeclared_denied = True

    result = {
        "fetched": "API_TOKEN",
        "length_bytes": len(token),
        "on_disk": Path("/run/secrets/API_TOKEN").exists(),  # expect False
        "undeclared_secret_denied": undeclared_denied,        # expect True
    }
    Path("/workspace/result.json").write_text(json.dumps(result, indent=2))
    print(json.dumps(result), flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
