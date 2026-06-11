#!/usr/bin/env python3
"""Minimal agent that calls OpenAI's gpt-4o-mini with workspace tools.

Sibling of ``examples/minimal-agent``. Same agent protocol shapes, same tool
set, same /workspace boundary — only the model and SDK differ.

Reads a ``WorkRequest`` from /workspace/input.json, runs an agentic loop with
``bash``, ``read_file``, and ``write_file`` tools that execute inside the
microVM's /workspace, and writes a ``WorkResult`` to /workspace/result.json.
Lifecycle signals stream to stderr as JSON lines.

OpenAI applies prompt caching automatically for prefixes ≥ 1024 tokens — no
client-side configuration needed.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

from openai import OpenAI
from pydantic import BaseModel

from protocol import (
    Constraints,
    LifecycleSignal,
    LifecycleSignalKind,
    WorkRequest,
    WorkResult,
    WorkStatus,
)

AGENT_ID = "minimal-agent-openai-1"
CONSTRAINTS_PATH = Path("/agent/constraints.json")
SYSTEM_PROMPT_PATH = Path("/agent/system_prompt.md")
INPUT_PATH = Path("/workspace/input.json")
RESULT_PATH = Path("/workspace/result.json")
WORKSPACE_ROOT = Path("/workspace").resolve()

SYSTEM_PROMPT = SYSTEM_PROMPT_PATH.read_text()
client = OpenAI(api_key=os.environ["OPENAI_API_KEY"])

TOOLS = [
    {
        "type": "function",
        "function": {
            "name": "bash",
            "description": "Run a shell command in /workspace. Returns exit code, stdout, and stderr.",
            "parameters": {
                "type": "object",
                "properties": {"command": {"type": "string"}},
                "required": ["command"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "read_file",
            "description": "Read a file from disk and return its contents. Path must be absolute.",
            "parameters": {
                "type": "object",
                "properties": {"path": {"type": "string"}},
                "required": ["path"],
            },
        },
    },
    {
        "type": "function",
        "function": {
            "name": "write_file",
            "description": "Write content to a file on disk. Creates or overwrites. Path must be absolute.",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string"},
                    "content": {"type": "string"},
                },
                "required": ["path", "content"],
            },
        },
    },
]


def now() -> datetime:
    return datetime.now(timezone.utc)


def emit(model: BaseModel) -> None:
    print(model.model_dump_json(), file=sys.stderr, flush=True)


def tool_env() -> dict[str, str]:
    blocked = ("API_KEY", "TOKEN", "SECRET", "CREDENTIAL")
    return {
        key: value
        for key, value in os.environ.items()
        if not any(marker in key.upper() for marker in blocked)
    }


def workspace_path(raw: str) -> Path:
    path = Path(raw)
    if not path.is_absolute():
        path = WORKSPACE_ROOT / path
    resolved = path.resolve(strict=False)
    if not resolved.is_relative_to(WORKSPACE_ROOT):
        raise ValueError(f"path must stay under {WORKSPACE_ROOT}: {raw}")
    return resolved


def execute_tool(name: str, args: dict) -> str:
    if name == "bash":
        r = subprocess.run(
            ["bash", "-c", args["command"]],
            capture_output=True,
            text=True,
            timeout=30,
            cwd="/workspace",
            env=tool_env(),
        )
        return f"exit_code={r.returncode}\nstdout:\n{r.stdout}\nstderr:\n{r.stderr}"
    if name == "read_file":
        return workspace_path(args["path"]).read_text()
    if name == "write_file":
        path = workspace_path(args["path"])
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(args["content"])
        return f"wrote {len(args['content'])} bytes to {args['path']}"
    return f"unknown tool: {name}"


def process(req: WorkRequest) -> WorkResult:
    max_tokens = (
        req.bounds.max_tokens
        if req.bounds and req.bounds.max_tokens
        else 16_000
    )
    messages: list[dict] = [
        {"role": "system", "content": SYSTEM_PROMPT},
        {"role": "user", "content": req.content},
    ]

    while True:
        msg = client.chat.completions.create(
            model="gpt-4o-mini",
            messages=messages,
            tools=TOOLS,
            max_tokens=max_tokens,
        )
        choice = msg.choices[0]
        # Append the assistant turn as a dict so subsequent iterations
        # see a uniform message shape.
        messages.append(choice.message.model_dump(exclude_unset=True))

        if not choice.message.tool_calls:
            break

        for tc in choice.message.tool_calls:
            args = json.loads(tc.function.arguments)
            messages.append({
                "role": "tool",
                "tool_call_id": tc.id,
                "content": execute_tool(tc.function.name, args),
            })

    final_text = choice.message.content or ""
    return WorkResult(
        request_id=req.request_id,
        status=WorkStatus.completed,
        content=final_text,
        completed_at=now(),
        audit_ref=req.audit_ref,
    )


def main() -> int:
    Constraints.model_validate_json(CONSTRAINTS_PATH.read_text())  # validate envelope

    emit(LifecycleSignal(
        signal=LifecycleSignalKind.ready,
        agent_id=AGENT_ID,
        observed_at=now(),
    ))

    req = WorkRequest.model_validate_json(INPUT_PATH.read_text())

    emit(LifecycleSignal(
        signal=LifecycleSignalKind.accepting,
        agent_id=AGENT_ID,
        observed_at=now(),
        request_id=req.request_id,
    ))

    result = process(req)
    RESULT_PATH.write_text(result.model_dump_json(indent=2))

    emit(LifecycleSignal(
        signal=LifecycleSignalKind.completed,
        agent_id=AGENT_ID,
        observed_at=now(),
        request_id=req.request_id,
    ))

    return 0


if __name__ == "__main__":
    sys.exit(main())
