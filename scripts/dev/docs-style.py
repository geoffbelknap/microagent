#!/usr/bin/env python3
"""Check docs prose against the banned-phrase list below.

Scans every markdown file under each target path. Targets may be files or
directories; the default is the docs/ directory of the enclosing git
repository, or the repository root when there is no docs/ directory. Code
blocks and inline code spans are ignored. Exits non-zero listing file:line
for each hit.
"""

from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys
from pathlib import Path


def repo_root() -> Path:
    result = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
    )
    if result.returncode == 0:
        return Path(result.stdout.strip())
    return Path.cwd()


def default_target() -> Path:
    root = repo_root()
    docs = root / "docs"
    return docs if docs.is_dir() else root


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


def markdown_files(target: Path) -> list[Path]:
    if target.is_file():
        return [target]
    return sorted(
        path
        for path in target.rglob("*.md")
        if ".git" not in path.parts and "node_modules" not in path.parts
    )


def check_file(path: Path) -> list[str]:
    problems: list[str] = []
    in_fence = False
    rel = os.path.relpath(path)
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
                problems.append(
                    f'{rel}:{lineno}: banned phrase "{match.group(0)}" — use {hint}'
                )
    return problems


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "targets",
        nargs="*",
        type=Path,
        help="markdown files or directories to scan (default: repo docs/ or root)",
    )
    args = parser.parse_args()
    targets = args.targets or [default_target()]

    files: list[Path] = []
    for target in targets:
        if not target.exists():
            print(f"{target}: no such file or directory", file=sys.stderr)
            return 2
        files.extend(markdown_files(target))

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
