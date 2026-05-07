#!/usr/bin/env python3
"""Check internal Markdown links in the repository."""

from __future__ import annotations

import os
import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
LINK_RE = re.compile(r"\[[^\]]+\]\(([^)]+)\)")


def markdown_files() -> list[Path]:
    files: list[Path] = []
    for base, dirs, names in os.walk(ROOT):
        path = Path(base)
        if ".git" in path.parts:
            continue
        dirs[:] = [name for name in dirs if name not in {".git", ".cache"}]
        for name in names:
            if name.endswith(".md") or name == "AGENTS.md":
                files.append(path / name)
    return files


def candidates_for(source: Path, link: str) -> list[Path]:
    link = link.split("#", 1)[0].strip()
    if not link or re.match(r"^[a-z][a-z0-9+.-]*:", link):
        return []
    if link.startswith("/"):
        target = ROOT / "docs" / link.lstrip("/").rstrip("/")
    else:
        target = (source.parent / link.rstrip("/")).resolve()
    candidates = [target]
    if not target.suffix:
        candidates.extend([target.with_suffix(".md"), target / "index.md"])
    return candidates


def main() -> int:
    missing: list[tuple[Path, str]] = []
    files = markdown_files()
    for source in files:
        text = source.read_text(encoding="utf-8")
        for match in LINK_RE.finditer(text):
            link = match.group(1)
            candidates = candidates_for(source, link)
            if candidates and not any(path.exists() for path in candidates):
                missing.append((source.relative_to(ROOT), link))
    if missing:
        for source, link in missing:
            print(f"{source}: missing {link}", file=sys.stderr)
        return 1
    print(f"checked {len(files)} markdown files; links ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
