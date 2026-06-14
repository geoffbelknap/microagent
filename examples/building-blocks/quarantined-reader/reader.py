#!/usr/bin/env python3
"""Read attacker-controlled text in a box that can't act on it.

This is the quarantined half of a dual-LLM design. It runs with `--network
isolated` (no egress) and has no tools — its only output is a fixed JSON schema.
Untrusted content (here, an email that tries to hijack the agent) goes in; only
structured fields come out. The privileged agent downstream consumes the JSON,
never the raw prose, so an injection has nothing to act on and nowhere to send.

The extraction here is deliberately simple. The security property is the box,
not the parser: swap this for a local-model call (see ../local-coder) and the
guarantees — isolated, tool-less, structured-output-only — are unchanged.
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

WORKSPACE = Path("/workspace")

# The contract with the privileged side. There is deliberately no field that
# carries an instruction, so commands hidden in the body have nowhere to land.
SCHEMA_FIELDS = ("from", "subject", "summary")


def extract(raw: str) -> dict:
    headers: dict[str, str] = {}
    for line in raw.splitlines():
        match = re.match(r"(From|Subject):\s*(.*)", line, re.IGNORECASE)
        if match:
            headers[match.group(1).lower()] = match.group(2).strip()
    body = raw.split("\n\n", 1)[-1].strip()
    fields = {
        "from": headers.get("from", ""),
        "subject": headers.get("subject", ""),
        "summary": " ".join(body.split()[:20]),
    }
    return {k: v for k, v in fields.items() if k in SCHEMA_FIELDS}


def main() -> int:
    raw = (WORKSPACE / "untrusted.txt").read_text()
    extracted = extract(raw)
    (WORKSPACE / "extracted.json").write_text(json.dumps(extracted, indent=2))
    print(json.dumps(extracted), flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
