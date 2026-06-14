#!/usr/bin/env python3
"""A coding agent powered by a local model, running inside a microVM.

No API key, no cloud. `microagent run/create --model <gguf>` serves the model
on the host and injects ``OPENAI_BASE_URL`` into the guest, so this agent is a
stock OpenAI client pointed at a model on your own machine. It reads the task
in /workspace, asks the model to fix calculator.py, runs the tests, and loops
on the failures until they pass (or it runs out of rounds).

The protocol is deliberately dumb so it works with small local models: ask for
the full corrected file in one code block, write it, run pytest, feed failures
back. No tool-calling required.
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from pathlib import Path

from openai import OpenAI

WORKSPACE = Path("/workspace")
TARGET = WORKSPACE / "calculator.py"
MAX_ROUNDS = 4

# OPENAI_BASE_URL is injected by `--model`; llama-server ignores the API key.
client = OpenAI(
    base_url=os.environ["OPENAI_BASE_URL"],
    api_key=os.environ.get("OPENAI_API_KEY", "local"),
)
MODEL = os.environ.get("MICROAGENT_MODEL", "local")

SYSTEM = (
    "You are a coding agent. When asked to fix a file, reply with the COMPLETE "
    "corrected contents of that file inside a single ```python code block and "
    "nothing else. Do not edit the tests."
)


def log(msg: str) -> None:
    print(msg, file=sys.stderr, flush=True)


def run_tests() -> tuple[bool, str]:
    proc = subprocess.run(
        ["python", "-m", "pytest", "-q"],
        cwd=WORKSPACE,
        capture_output=True,
        text=True,
    )
    return proc.returncode == 0, proc.stdout + proc.stderr


def ask(messages: list[dict]) -> str:
    resp = client.chat.completions.create(
        model=MODEL, messages=messages, temperature=0.2
    )
    return resp.choices[0].message.content or ""


def extract_code(text: str) -> str | None:
    match = re.search(r"```(?:python)?\s*\n(.*?)```", text, re.S)
    return match.group(1) if match else None


def main() -> int:
    messages = [
        {"role": "system", "content": SYSTEM},
        {
            "role": "user",
            "content": (
                f"{(WORKSPACE / 'TASK.md').read_text()}\n\n"
                f"--- calculator.py ---\n{TARGET.read_text()}\n\n"
                f"--- test_calculator.py ---\n"
                f"{(WORKSPACE / 'test_calculator.py').read_text()}"
            ),
        },
    ]

    rounds: list[dict] = []
    passed = False
    for attempt in range(1, MAX_ROUNDS + 1):
        reply = ask(messages)
        code = extract_code(reply)
        if code:
            TARGET.write_text(code)

        passed, output = run_tests()
        rounds.append({"round": attempt, "wrote_file": code is not None, "passed": passed})
        log(f"round {attempt}: tests_passed={passed}")
        if passed:
            break

        messages.append({"role": "assistant", "content": reply})
        messages.append({
            "role": "user",
            "content": (
                f"The tests still fail:\n\n{output}\n\n"
                "Reply with the full corrected calculator.py again."
            ),
        })

    (WORKSPACE / "result.json").write_text(
        json.dumps({"passed": passed, "rounds": rounds}, indent=2)
    )
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
