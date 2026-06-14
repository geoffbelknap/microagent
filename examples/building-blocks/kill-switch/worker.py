#!/usr/bin/env python3
"""A long-running agent — the kind you'd quarantine on suspicion.

It loops doing its "work" and, each round, checks whether it can still reach the
outside world, so you can watch the kill switch land. Boot it with a normal
network, then `microagent quarantine` it from the host: egress flips to BLOCKED,
the VM keeps running, and its disk, logs, and events are preserved for forensics.
"""

from __future__ import annotations

import socket
import time
from datetime import datetime, timezone


def egress_ok() -> bool:
    try:
        socket.setdefaulttimeout(3)
        socket.create_connection(("1.1.1.1", 53)).close()
        return True
    except OSError:
        return False


def main() -> None:
    round_num = 0
    while True:
        round_num += 1
        stamp = datetime.now(timezone.utc).isoformat()
        state = "ok" if egress_ok() else "BLOCKED"
        print(f"{stamp} round={round_num} egress={state}", flush=True)
        time.sleep(2)


if __name__ == "__main__":
    main()
