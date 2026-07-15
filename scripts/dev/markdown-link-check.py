#!/usr/bin/env python3
"""Check internal Markdown links in one or more repositories.

Each positional argument is a repository root to scan; the default is the
enclosing git repository. Absolute links ("/guides/...") resolve against
the root's docs/ directory.
"""

from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys
from pathlib import Path


LINK_RE = re.compile(r"\[[^\]]+\]\(([^)]+)\)")


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


def markdown_files(root: Path) -> list[Path]:
    files: list[Path] = []
    for base, dirs, names in os.walk(root):
        path = Path(base)
        dirs[:] = [
            name for name in dirs if name not in {".git", ".cache", "node_modules"}
        ]
        for name in names:
            if name.endswith(".md"):
                files.append(path / name)
    return files


def candidates_for(root: Path, source: Path, link: str) -> list[Path]:
    link = link.split("#", 1)[0].strip()
    if not link or re.match(r"^[a-z][a-z0-9+.-]*:", link):
        return []
    if link.startswith("/"):
        target = root / "docs" / link.lstrip("/").rstrip("/")
    else:
        target = (source.parent / link.rstrip("/")).resolve()
    candidates = [target]
    if not target.suffix:
        candidates.extend([target.with_suffix(".md"), target / "index.md"])
    return candidates


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "roots",
        nargs="*",
        type=Path,
        help="repository roots to scan (default: the enclosing git repository)",
    )
    args = parser.parse_args()
    roots = args.roots or [repo_root()]

    missing: list[tuple[str, str]] = []
    total = 0
    for root in roots:
        if not root.is_dir():
            print(f"{root}: no such directory", file=sys.stderr)
            return 2
        files = markdown_files(root)
        total += len(files)
        for source in files:
            text = source.read_text(encoding="utf-8")
            for match in LINK_RE.finditer(text):
                link = match.group(1)
                candidates = candidates_for(root, source, link)
                if candidates and not any(path.exists() for path in candidates):
                    missing.append((os.path.relpath(source), link))
    if missing:
        for source, link in missing:
            print(f"{source}: missing {link}", file=sys.stderr)
        return 1
    print(f"checked {total} markdown files; links ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
