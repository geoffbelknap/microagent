#!/usr/bin/env python3
"""Reject PRs that edit CHANGELOG.md directly instead of adding a fragment.

Compares this branch against its PR base (from $GITHUB_BASE_REF, or --base
locally). A CHANGELOG.md edit is only allowed when the same diff also
deletes a .changelog.d/ fragment, which is what
scripts/dev/changelog-assemble.py does at release time. Every other
CHANGELOG.md edit should instead be a new .changelog.d/<slug>.md file (see
.changelog.d/README.md). Not a pull request (no base to compare against):
does nothing.
"""

from __future__ import annotations

import argparse
import os
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def run(cmd: list[str]) -> str | None:
    result = subprocess.run(cmd, cwd=ROOT, text=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    if result.returncode != 0:
        return None
    return result.stdout


def resolve_base(base: str) -> str | None:
    for candidate in (f"origin/{base}", base):
        if run(["git", "rev-parse", "--verify", "--quiet", candidate]) is not None:
            return candidate
    return None


def changed_paths(base: str) -> list[tuple[str, str]]:
    output = run(["git", "diff", "--name-status", f"{base}...HEAD"]) or ""
    changes = []
    for line in output.splitlines():
        status, _, path = line.partition("\t")
        changes.append((status, path.split("\t")[-1]))
    return changes


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", default=os.environ.get("GITHUB_BASE_REF"), help="base branch to diff against")
    args = parser.parse_args()

    if not args.base:
        print("changelog fragment check: not a pull request, skipping")
        return 0

    base = resolve_base(args.base)
    if base is None:
        print(f"changelog fragment check: could not resolve base {args.base!r}, skipping")
        return 0

    changes = changed_paths(base)
    if not any(path == "CHANGELOG.md" for _, path in changes):
        print("changelog fragment check: CHANGELOG.md untouched")
        return 0

    fragment_deleted = any(
        status.startswith("D") and path.startswith(".changelog.d/") for status, path in changes
    )
    if fragment_deleted:
        print("changelog fragment check: CHANGELOG.md edit assembles fragments, ok")
        return 0

    print(
        "CHANGELOG.md was edited directly. Add a .changelog.d/<slug>.md fragment "
        "instead (see .changelog.d/README.md) - CHANGELOG.md is only written by "
        "scripts/dev/changelog-assemble.py at release time.",
        file=sys.stderr,
    )
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
