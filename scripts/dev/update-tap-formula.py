#!/usr/bin/env python3
"""Update a Homebrew tap formula from microagent's release workflows.

Bumps the formula's version and revision, and rewrites its caveats block
from the repo-owned source file in packaging/homebrew/ so install-time
text can never drift from the product again (the text is reviewed in the
same pull request as the behavior change that motivates it).

Caveats semantics:
- source file with text  -> the formula carries exactly that text in a
  `def caveats` heredoc (inserted before `test do` when absent)
- empty source file      -> the formula has no caveats block at all
- missing source file    -> caveats are left untouched, with a warning
  (keeps re-dispatched updates for tags that predate the source files
  working exactly as before)

Fails loudly when the version or revision anchors are not found exactly
once, matching the previous inline behavior.
"""

import argparse
import os
import re
import sys


def rewrite_caveats(text: str, caveats: str, formula: str) -> str:
    block_re = re.compile(r"\n  def caveats\n    <<~EOS\n.*?\n    EOS\n  end\n", re.S)
    if caveats.strip():
        body = "".join(f"      {line}".rstrip() + "\n" for line in caveats.strip("\n").split("\n"))
        block = f"\n  def caveats\n    <<~EOS\n{body}    EOS\n  end\n"
        if block_re.search(text):
            return block_re.sub(block, text, count=1)
        anchor = "\n  test do\n"
        if anchor not in text:
            raise SystemExit(f"{formula}: no caveats block and no `test do` anchor to insert before")
        return text.replace(anchor, block + anchor, 1)
    if block_re.search(text):
        return block_re.sub("\n", text, count=1)
    return text


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--formula", required=True, help="path to the tap formula file to update")
    parser.add_argument("--version", required=True)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--caveats", required=True, help="path to the repo-owned caveats source file")
    args = parser.parse_args()

    with open(args.formula) as fh:
        text = fh.read()

    text, n_rev = re.subn(r'revision: "[0-9a-f]{40}"', f'revision: "{args.revision}"', text)
    text, n_ver = re.subn(r'\n  version "[^"]+"', f'\n  version "{args.version}"', text)
    if n_rev != 1 or n_ver != 1:
        raise SystemExit(f"expected exactly one revision and one version in {args.formula}, got {n_rev}/{n_ver}")

    if os.path.exists(args.caveats):
        with open(args.caveats) as fh:
            text = rewrite_caveats(text, fh.read(), args.formula)
    else:
        print(f"warning: {args.caveats} not found; leaving caveats untouched", file=sys.stderr)

    with open(args.formula, "w") as fh:
        fh.write(text)


if __name__ == "__main__":
    main()
