#!/usr/bin/env python3
"""Check that CLI docs stay aligned with `microagent help`."""

from __future__ import annotations

import os
import re
import subprocess
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
DOCS_CLI = ROOT / "docs" / "cli"

DOCUMENTED_SPECIAL_PAGES = {"index", "serve", "spec"}
UNDOCUMENTED_HELP_COMMANDS = {"help", "exec"}
COMMAND_DOC_ALIASES = {
    "inspect": "status",
    "rm": "delete",
    "rootfs build": "rootfs",
    "kernel install": "kernel",
    "kernel verify": "kernel",
}
INTENTIONAL_REQUEST_JSON_EXAMPLES = {
    "microagent create --json request.json",
}


def microagent_help() -> str:
    result = subprocess.run(
        ["go", "run", "./cmd/microagent", "help", "all"],
        cwd=ROOT,
        check=True,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    return result.stdout


def parse_help_commands(help_text: str) -> set[str]:
    commands: set[str] = set()
    section = ""
    for line in help_text.splitlines():
        if line == "Commands:":
            section = "commands"
            continue
        if line == "Advanced:":
            section = "advanced"
            continue
        if line == "Options:":
            section = ""
            continue
        if section not in {"commands", "advanced"} or not line.startswith("  "):
            continue
        stripped = line.strip()
        if not stripped:
            continue
        name = re.split(r"\s{2,}", stripped, maxsplit=1)[0].split(",", maxsplit=1)[0]
        commands.add(name)
    return commands


def doc_stem_for_command(command: str) -> str:
    return COMMAND_DOC_ALIASES.get(command, command.split()[0])


def documented_cli_pages() -> set[str]:
    return {path.stem for path in DOCS_CLI.glob("*.md")}


def check_help_has_docs() -> list[str]:
    help_commands = parse_help_commands(microagent_help())
    pages = documented_cli_pages()
    errors: list[str] = []
    for command in sorted(help_commands):
        if command in UNDOCUMENTED_HELP_COMMANDS:
            continue
        page = doc_stem_for_command(command)
        if page not in pages:
            errors.append(f"help command {command!r} is missing docs/cli/{page}.md")
    return errors


def check_docs_have_commands() -> list[str]:
    help_commands = parse_help_commands(microagent_help())
    command_pages = {doc_stem_for_command(command) for command in help_commands}
    allowed_pages = command_pages | DOCUMENTED_SPECIAL_PAGES
    errors: list[str] = []
    for page in sorted(documented_cli_pages()):
        if page not in allowed_pages:
            errors.append(f"docs/cli/{page}.md does not correspond to a help command")
    return errors


def markdown_files() -> list[Path]:
    files: list[Path] = []
    for base, dirs, names in os.walk(ROOT):
        path = Path(base)
        if ".git" in path.parts:
            continue
        dirs[:] = [name for name in dirs if name not in {".git", ".cache"}]
        for name in names:
            if name.endswith(".md"):
                files.append(path / name)
    return files


def shell_examples(text: str) -> list[str]:
    examples: list[str] = []
    in_fence = False
    fence_lang = ""
    for line in text.splitlines():
        if line.startswith("```"):
            if in_fence:
                in_fence = False
                fence_lang = ""
            else:
                in_fence = True
                fence_lang = line[3:].strip()
            continue
        if in_fence and fence_lang in {"bash", "sh", "text", ""}:
            stripped = line.strip()
            if stripped.startswith("$ "):
                stripped = stripped[2:].strip()
            if stripped.startswith("microagent "):
                examples.append(stripped)
    return examples


def check_json_flag_examples() -> list[str]:
    errors: list[str] = []
    pattern = re.compile(r"\bmicroagent\s+(?!-[^\s]*\s)*[a-z][^\n]*\s--json(?:\s|$)")
    for path in markdown_files():
        text = path.read_text(encoding="utf-8")
        for example in shell_examples(text):
            if example in INTENTIONAL_REQUEST_JSON_EXAMPLES:
                continue
            if pattern.search(example):
                rel = path.relative_to(ROOT)
                errors.append(
                    f"{rel}: use global JSON output as `microagent --json <command>`: {example}"
                )
    return errors


def main() -> int:
    errors = []
    errors.extend(check_help_has_docs())
    errors.extend(check_docs_have_commands())
    errors.extend(check_json_flag_examples())
    if errors:
        for error in errors:
            print(error, file=sys.stderr)
        return 1
    print("CLI docs match help output and JSON flag conventions")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
