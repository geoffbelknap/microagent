#!/usr/bin/env python3
"""Prove each task runs on a clean, disposable disk.

Every `microagent run` is its own microVM with a fresh rootfs and an identity
(runtimeID = --name) that microagent records host-side in events.json. Nothing a
previous task wrote survives into the next one. This task looks for a marker a
prior task might have left (there should never be one), then leaves its own —
which the next task still won't see.
"""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path

MARKER = Path("/workspace/.prev-task")


def main() -> int:
    saw_previous = MARKER.read_text().strip() if MARKER.exists() else None
    task = os.environ.get("TASK_LABEL", "unknown")
    MARKER.write_text(task)

    result = {"task": task, "saw_previous_task": saw_previous}
    Path("/workspace/result.json").write_text(json.dumps(result, indent=2))
    print(json.dumps(result), flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
