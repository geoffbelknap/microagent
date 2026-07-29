#!/usr/bin/env python3
"""Check docs prose against the banned-phrase list below.

Scans every markdown file under each target path. Targets may be files or
directories; the default is the docs/ directory of the enclosing git
repository, or the repository root when there is no docs/ directory. Code
blocks and inline code spans are ignored. Exits non-zero listing file:line
for each hit.

With --max-sentence-words N, additionally flags prose sentences longer than
N words. The length check reads paragraph prose only: code blocks, tables,
headings, list items (including their indented continuation lines), and
block quotes are skipped, an inline code span or a link counts as one word,
and frontmatter is ignored.
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
    (r"(?i)\be\.g\.", '"for example"'),
    (r"(?i)\bi\.e\.", '"that is"'),
    (r"(?i)\betc\.", "name the rest, or end the list one item earlier"),
]

RULES = [(re.compile(pat), hint) for pat, hint in BANNED]
INLINE_CODE = re.compile(r"`[^`]*`")
MD_LINK = re.compile(r"\[([^\]]*)\]\([^)]*\)")
NON_PROSE = re.compile(r"^(?:[|#>*+-]|\d+\.\s)")
SENTENCE_END = re.compile(r"(?:(?<=[.!?])|(?<=[.!?][\"')\]]))\s+")


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


def prose_paragraphs(path: Path) -> list[tuple[int, str]]:
    """Paragraphs of plain prose as (starting line number, text) pairs."""
    lines = path.read_text(encoding="utf-8").splitlines()
    start = 0
    if lines and lines[0] == "---":
        for i in range(1, len(lines)):
            if lines[i] == "---":
                start = i + 1
                break
    paragraphs: list[tuple[int, str]] = []
    current: list[str] = []
    current_start = 0
    in_fence = False

    def flush() -> None:
        if current:
            paragraphs.append((current_start, " ".join(current)))
            current.clear()

    for lineno in range(start, len(lines)):
        line = lines[lineno]
        stripped = line.strip()
        if stripped.startswith("```"):
            in_fence = not in_fence
            flush()
            continue
        indented = line[:1].isspace()
        if in_fence or not stripped or indented or NON_PROSE.match(stripped):
            flush()
            continue
        if not current:
            current_start = lineno + 1
        current.append(stripped)
    flush()
    return paragraphs


def check_sentence_length(path: Path, max_words: int) -> list[str]:
    problems: list[str] = []
    rel = os.path.relpath(path)
    for lineno, paragraph in prose_paragraphs(path):
        text = INLINE_CODE.sub("CODE", paragraph)
        text = MD_LINK.sub(r"\1", text)
        for sentence in SENTENCE_END.split(text):
            count = sum(
                1 for token in sentence.split() if any(c.isalnum() for c in token)
            )
            if count > max_words:
                problems.append(
                    f"{rel}:{lineno}: {count}-word sentence (max {max_words}):"
                    f' "{sentence[:80]}..."'
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
    parser.add_argument(
        "--max-sentence-words",
        type=int,
        default=0,
        metavar="N",
        help="also flag prose sentences longer than N words (0 disables)",
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
        if args.max_sentence_words > 0:
            problems.extend(check_sentence_length(path, args.max_sentence_words))
    if problems:
        print("docs style violations:", file=sys.stderr)
        for problem in problems:
            print(f"  {problem}", file=sys.stderr)
        return 1
    print(f"docs style ok ({len(files)} files)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
