#!/usr/bin/env python3
"""Check docs prose against the banned-phrase list below.

Scans every markdown file under docs/. Code blocks and inline code spans are
ignored. Exits non-zero listing file:line for each hit.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
DOCS = ROOT / "docs"

# (regex, what to do instead).
BANNED: list[tuple[str, str]] = [
    (r"(?i)\band friends\b", 'name the items, or "and the related commands"'),
    (r"(?i)\bin plain terms\b", "drop the framing and just be plain"),
    (r"(?i)\bput simply\b", "drop the framing and just be plain"),
    (r"(?i)\bclear-eyed\b", "state the behavior without the drama"),
    (r"(?i)\bit(?:'|’)?s important to\b", "say the thing; importance should be evident"),
    (r"(?i)\bit is important to\b", "say the thing; importance should be evident"),
    (r"(?i)\bworth noting\b", "just note it"),
    (r"(?i)(?<![Aa] )(?<![Tt]he )\bnote that\b", "drop it; state the fact directly"),
    (r"(?i)\bsimply\b", 'cut "simply"'),
    (r"(?i)\bseamless", "describe what happens instead"),
    (r"(?i)\bleverag(?:e|es|ed|ing)\b", '"use"'),
    (r"(?i)\bdelve\b", '"look at" / "cover"'),
    (r"(?i)\btextbook\b", "state the risk plainly"),
    (r"(?i)\b(?:is|that's) the (?:whole )?point\b", "state the behavior, not its justification"),
    (r"(?i)\bthe whole point\b", "state the behavior, not its justification"),
    (r"(?i)\bis the core of\b", "state the behavior, not its justification"),
    (r"(?i)\bstamp out\b", "plain verbs"),
    (r"(?i)\bconspicuous\b", '"unusual" / "recorded"'),
    (r"(?i)\bwander\b", "plain verbs"),
    (r"Flags you'll actually use", '"Common flags"'),
]

RULES = [(re.compile(pat), hint) for pat, hint in BANNED]
INLINE_CODE = re.compile(r"`[^`]*`")


def check_file(path: Path) -> list[str]:
    problems: list[str] = []
    in_fence = False
    for lineno, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        stripped = line.lstrip()
        if stripped.startswith("```"):
            in_fence = not in_fence
            continue
        if in_fence:
            continue
        prose = INLINE_CODE.sub("", line)
        for rule, hint in RULES:
            match = rule.search(prose)
            if match:
                rel = path.relative_to(ROOT)
                problems.append(
                    f'{rel}:{lineno}: banned phrase "{match.group(0)}" — use {hint}'
                )
    return problems


def main() -> int:
    files = sorted(DOCS.rglob("*.md"))
    problems: list[str] = []
    for path in files:
        problems.extend(check_file(path))
    if problems:
        print("docs style violations:", file=sys.stderr)
        for problem in problems:
            print(f"  {problem}", file=sys.stderr)
        return 1
    print(f"docs style ok ({len(files)} files)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
