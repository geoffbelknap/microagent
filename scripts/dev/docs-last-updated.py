#!/usr/bin/env python3
"""Stamp docs pages with a rendered last-updated date.

The positional argument is the repository root to process; the default is
the enclosing git repository. Pages are the markdown files under the
root's docs/ directory.
"""

from __future__ import annotations

import argparse
import datetime as dt
import os
import posixpath
import re
import subprocess
import sys
from pathlib import Path


ROOT = Path.cwd()
DOCS = ROOT / "docs"
STAMP_RE = re.compile(
    r"(?:<!-- docs-last-updated -->\n)?_Last updated: \d{4}-\d{2}-\d{2}_\n\n"
)
LINK_RE = re.compile(r"\]\(([^)\s]+)\)")
SCHEME_RE = re.compile(r"^[a-z][a-z0-9+.-]*:")


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


def run(cmd: list[str], env: dict[str, str] | None = None) -> str:
    result = subprocess.run(
        cmd,
        cwd=ROOT,
        check=True,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        env=env,
    )
    return result.stdout.strip()


def docs_pages() -> list[Path]:
    if not DOCS.is_dir():
        return []
    return sorted(DOCS.rglob("*.md"))


# Dates stamped while a file is dirty come from today(), which is UTC. Commit
# dates must be read on the same UTC basis: %cs uses the commit's recorded
# local offset, so a commit made in the evening US Pacific time reads back one
# day earlier than the UTC stamp and --check falsely reports it as stale.
UTC_DATE_ARGS = ["--date=format-local:%Y-%m-%d", "--format=%cd"]


def run_utc(cmd: list[str]) -> str:
    return run(cmd, env={**os.environ, "TZ": "UTC"})


def git_date(path: Path) -> str:
    rel = str(path.relative_to(ROOT))
    for commit in git_history(rel):
        if commit_is_substantive(commit, rel):
            return run_utc(["git", "show", "-s", *UTC_DATE_ARGS, commit])
    date = run_utc(["git", "log", "-1", *UTC_DATE_ARGS, "--", rel])
    if date:
        return date
    return today()


def git_history(rel: str) -> list[str]:
    output = run(["git", "log", "--format=%H", "--", rel])
    return [line for line in output.splitlines() if line]


def git_file(commitish: str, rel: str) -> str | None:
    result = subprocess.run(
        ["git", "show", f"{commitish}:{rel}"],
        cwd=ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
    )
    if result.returncode != 0:
        return None
    return result.stdout


def canonical_link(link: str, page_dir: str) -> str:
    """Reduce a docs link to its target, independent of how it was spelled.

    A site-absolute link ("/cli/doctor/#flags") and a relative Markdown link
    ("../cli/doctor.md#flags") name the same page. Comparing the canonical
    form means a change of link style alone does not move the stamp.
    """
    if SCHEME_RE.match(link) or link.startswith(("#", "//")):
        return link
    target, sep, fragment = link.partition("#")
    if target.startswith("/"):
        page = target.strip("/")
    elif target.endswith(".md"):
        page = posixpath.normpath(posixpath.join(page_dir, target))[: -len(".md")]
        if page == "index":
            page = ""
        elif page.endswith("/index"):
            page = page[: -len("/index")]
    else:
        return link
    return f"{page}{sep}{fragment}"


def normalize_for_substantive_compare(text: str | None, rel: str) -> str | None:
    if text is None:
        return None
    frontmatter, body = split_frontmatter(text)
    body = STAMP_RE.sub("", body.lstrip("\n"), count=1)
    page_dir = posixpath.dirname(posixpath.relpath(rel, "docs"))
    body = LINK_RE.sub(lambda m: f"]({canonical_link(m.group(1), page_dir)})", body)
    if frontmatter:
        return f"{frontmatter}\n{body}"
    return body


def commit_is_substantive(commit: str, rel: str) -> bool:
    current = normalize_for_substantive_compare(git_file(commit, rel), rel)
    previous = normalize_for_substantive_compare(git_file(f"{commit}^", rel), rel)
    return current != previous


def today() -> str:
    return dt.datetime.now(dt.UTC).date().isoformat()


def git_dirty(path: Path) -> bool:
    rel = str(path.relative_to(ROOT))
    unstaged = subprocess.run(["git", "diff", "--quiet", "--", rel], cwd=ROOT)
    staged = subprocess.run(["git", "diff", "--cached", "--quiet", "--", rel], cwd=ROOT)
    return unstaged.returncode != 0 or staged.returncode != 0


def working_tree_substantive_dirty(path: Path) -> bool:
    if not git_dirty(path):
        return False
    rel = str(path.relative_to(ROOT))
    current = normalize_for_substantive_compare(path.read_text(encoding="utf-8"), rel)
    head = normalize_for_substantive_compare(git_file("HEAD", rel), rel)
    return current != head


def split_frontmatter(text: str) -> tuple[str, str]:
    lines = text.splitlines(keepends=True)
    if not lines or lines[0].strip() != "---":
        return "", text
    for index in range(1, len(lines)):
        if lines[index].strip() == "---":
            return "".join(lines[: index + 1]), "".join(lines[index + 1 :])
    return "", text


def stamped_text(path: Path) -> str:
    original = path.read_text(encoding="utf-8")
    frontmatter, body = split_frontmatter(original)
    body = body.lstrip("\n")
    has_stamp = STAMP_RE.search(body) is not None
    body = STAMP_RE.sub("", body, count=1)
    stamp_date = today() if working_tree_substantive_dirty(path) or not has_stamp else git_date(path)
    stamp = f"<!-- docs-last-updated -->\n_Last updated: {stamp_date}_\n\n"
    if frontmatter:
        return f"{frontmatter}\n{stamp}{body}"
    return f"{stamp}{body}"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="fail if docs stamps are stale")
    parser.add_argument(
        "root",
        nargs="?",
        type=Path,
        help="repository root to process (default: the enclosing git repository)",
    )
    args = parser.parse_args()

    global ROOT, DOCS
    ROOT = (args.root or repo_root()).resolve()
    DOCS = ROOT / "docs"

    stale: list[Path] = []
    for path in docs_pages():
        updated = stamped_text(path)
        current = path.read_text(encoding="utf-8")
        if updated == current:
            continue
        if args.check:
            stale.append(path.relative_to(ROOT))
        else:
            path.write_text(updated, encoding="utf-8")
    if stale:
        for path in stale:
            print(f"{path}: last-updated stamp is stale", file=sys.stderr)
        print("re-run docs-last-updated.py without --check to refresh", file=sys.stderr)
        return 1
    if args.check:
        print("docs last-updated stamps ok")
    else:
        print(f"updated {len(docs_pages())} docs last-updated stamps")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
