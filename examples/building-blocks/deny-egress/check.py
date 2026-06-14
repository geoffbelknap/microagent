#!/usr/bin/env python3
"""Proof that an isolated workspace has no way out.

Run under `--network isolated`, the guest has no network device at all. This
script tries to reach the internet (it must fail), then does its real local
work (it must succeed), and writes the verdict to /workspace/result.json.

The exit code is the assertion: nonzero if egress was NOT blocked, because an
agent that can phone home defeats the entire point.
"""

from __future__ import annotations

import json
import socket
import sys
from pathlib import Path

WORKSPACE = Path("/workspace")


def egress_blocked() -> tuple[bool, str]:
    """Try one outbound connection. Blocked is the result we want."""
    try:
        socket.setdefaulttimeout(3)
        socket.create_connection(("1.1.1.1", 53)).close()
        return False, "reached 1.1.1.1:53 — egress is OPEN"
    except OSError as exc:
        return True, f"{type(exc).__name__}: {exc}"


def local_work() -> int:
    """Stand-in for the agent's real job: summarize some local data."""
    return len((WORKSPACE / "notes.txt").read_text().split())


def main() -> int:
    blocked, detail = egress_blocked()
    result = {
        "egress_blocked": blocked,
        "egress_detail": detail,
        "local_work_ok": True,
        "word_count": local_work(),
    }
    (WORKSPACE / "result.json").write_text(json.dumps(result, indent=2))
    print(json.dumps(result), flush=True)
    return 0 if blocked else 1


if __name__ == "__main__":
    sys.exit(main())
