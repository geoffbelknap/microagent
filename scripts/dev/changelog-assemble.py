#!/usr/bin/env python3
"""Assemble .changelog.d/ fragments into CHANGELOG.md's Unreleased section.

Each fragment is a Markdown file with one or more level-3 (###) sections,
written by a change instead of editing CHANGELOG.md directly (see
.changelog.d/README.md). This script concatenates the fragments into
CHANGELOG.md under the Unreleased heading, in filename order, and deletes
the fragment files. Run it at release time; it is the only time
CHANGELOG.md is written.
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FRAGMENT_DIR = ROOT / ".changelog.d"
CHANGELOG = ROOT / "CHANGELOG.md"
UNRELEASED_HEADING = "## Unreleased\n"
KEEP = {"README.md"}


def fragments() -> list[Path]:
    if not FRAGMENT_DIR.is_dir():
        return []
    return sorted(path for path in FRAGMENT_DIR.glob("*.md") if path.name not in KEEP)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--check", action="store_true", help="list pending fragments without assembling them"
    )
    args = parser.parse_args()

    frags = fragments()
    if args.check:
        for frag in frags:
            print(frag.relative_to(ROOT))
        return 0

    if not frags:
        print("no changelog fragments to assemble", file=sys.stderr)
        return 0

    changelog = CHANGELOG.read_text(encoding="utf-8")
    if UNRELEASED_HEADING not in changelog:
        print(f"{CHANGELOG.relative_to(ROOT)}: missing {UNRELEASED_HEADING.strip()!r} heading", file=sys.stderr)
        return 1

    assembled = "\n".join(frag.read_text(encoding="utf-8").strip("\n") + "\n" for frag in frags)
    changelog = changelog.replace(UNRELEASED_HEADING, UNRELEASED_HEADING + "\n" + assembled, 1)
    CHANGELOG.write_text(changelog, encoding="utf-8")

    for frag in frags:
        frag.unlink()

    print(f"assembled {len(frags)} fragment(s) into {CHANGELOG.relative_to(ROOT)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
